package discord

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/disgoorg/disgo"
	"github.com/disgoorg/disgo/bot"
	discordmodel "github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/gateway"
	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/godave/golibdave"
	"github.com/disgoorg/snowflake/v2"
)

// DisgoClient implements Client using the disgo library with DAVE E2EE support.
type DisgoClient struct {
	token string
	// client is set once in NewDisgoClient and never reassigned. All methods
	// may read it without holding mu. Do NOT reassign after construction.
	client   *bot.Client
	handlers []EventHandler
	mu       sync.RWMutex
	ready    bool
	botID    string
}

// NewDisgoClient creates a new DisgoClient with the given bot token.
func NewDisgoClient(token string) (*DisgoClient, error) {
	dc := &DisgoClient{
		token: token,
	}

	client, err := disgo.New(token,
		bot.WithGatewayConfigOpts(
			gateway.WithIntents(
				gateway.IntentGuilds,
				gateway.IntentGuildMessages,
				gateway.IntentGuildVoiceStates,
				gateway.IntentMessageContent,
				gateway.IntentDirectMessages,
			),
		),
		bot.WithVoiceManagerConfigOpts(
			voice.WithDaveSessionCreateFunc(golibdave.NewSession),
		),
		bot.WithEventListenerFunc(dc.onReady),
		bot.WithEventListenerFunc(dc.onGuildAvailable),
		bot.WithEventListenerFunc(dc.onMessageCreate),
		bot.WithEventListenerFunc(dc.onGuildVoiceJoin),
		bot.WithEventListenerFunc(dc.onGuildVoiceMove),
		bot.WithEventListenerFunc(dc.onGuildVoiceLeave),
		bot.WithLogger(slog.Default()),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create disgo client: %w", err)
	}

	dc.client = client

	return dc, nil
}

// Connect opens the gateway connection.
func (dc *DisgoClient) Connect(ctx context.Context) error {
	return dc.client.OpenGateway(ctx)
}

// Disconnect closes the client and resets ready state so that IsReady()
// returns false until the next successful Connect+onReady cycle.
// botID is preserved so that concurrent callers (e.g. event handlers
// processing final events) still filter the bot correctly.
func (dc *DisgoClient) Disconnect(ctx context.Context) error {
	dc.client.Close(ctx)

	dc.mu.Lock()
	dc.ready = false
	dc.mu.Unlock()

	return nil
}

// SendMessage sends a text message to a channel.
func (dc *DisgoClient) SendMessage(channelID, content string) error {
	cid, err := snowflake.Parse(channelID)
	if err != nil {
		return fmt.Errorf("invalid channel ID %s: %w", channelID, err)
	}

	_, err = dc.client.Rest.CreateMessage(cid, discordmodel.MessageCreate{
		Content: content,
	})
	if err != nil {
		return fmt.Errorf("failed to send message: %w", err)
	}

	return nil
}

// GetUser retrieves a user by ID.
func (dc *DisgoClient) GetUser(userID string) (*User, error) {
	uid, err := snowflake.Parse(userID)
	if err != nil {
		return nil, fmt.Errorf("invalid user ID %s: %w", userID, err)
	}

	u, err := dc.client.Rest.GetUser(uid)
	if err != nil {
		return nil, fmt.Errorf("failed to get user: %w", err)
	}

	return &User{
		ID:       u.ID.String(),
		Username: u.Username,
		Bot:      u.Bot,
	}, nil
}

// CreateDM creates a DM channel and returns its ID.
func (dc *DisgoClient) CreateDM(userID string) (string, error) {
	uid, err := snowflake.Parse(userID)
	if err != nil {
		return "", fmt.Errorf("invalid user ID %s: %w", userID, err)
	}

	ch, err := dc.client.Rest.CreateDMChannel(uid)
	if err != nil {
		return "", fmt.Errorf("failed to create DM channel: %w", err)
	}

	return ch.ID().String(), nil
}

// GetGuild retrieves guild information including voice states from cache.
func (dc *DisgoClient) GetGuild(guildID string) (*Guild, error) {
	gid, err := snowflake.Parse(guildID)
	if err != nil {
		return nil, fmt.Errorf("invalid guild ID %s: %w", guildID, err)
	}

	// Try cache first
	g, ok := dc.client.Caches.Guild(gid)
	if !ok {
		// Fall back to REST (won't have voice states)
		rg, restErr := dc.client.Rest.GetGuild(gid, false)
		if restErr != nil {
			return nil, fmt.Errorf("failed to get guild: %w", restErr)
		}

		return &Guild{
			ID:   rg.ID.String(),
			Name: rg.Name,
		}, nil
	}

	guild := &Guild{
		ID:   g.ID.String(),
		Name: g.Name,
	}

	// Get voice states from cache using iter.Seq
	for vs := range dc.client.Caches.VoiceStates(gid) {
		channelID := ""
		if vs.ChannelID != nil {
			channelID = vs.ChannelID.String()
		}
		guild.VoiceStates = append(guild.VoiceStates, VoiceState{
			UserID:    vs.UserID.String(),
			ChannelID: channelID,
			GuildID:   vs.GuildID.String(),
		})
	}

	return guild, nil
}

// GetBotUserID returns the bot's own user ID.
func (dc *DisgoClient) GetBotUserID() string {
	dc.mu.RLock()
	defer dc.mu.RUnlock()

	return dc.botID
}

// IsReady returns true if the gateway is connected and ready.
func (dc *DisgoClient) IsReady() bool {
	dc.mu.RLock()
	defer dc.mu.RUnlock()

	return dc.ready
}

// CreateVoiceConnection creates a new VoiceConnection for the given guild.
func (dc *DisgoClient) CreateVoiceConnection(guildID string) (VoiceConnection, error) {
	gid, err := snowflake.Parse(guildID)
	if err != nil {
		return nil, fmt.Errorf("invalid guild ID %s: %w", guildID, err)
	}

	return &DisgoVoiceConnection{
		client:  dc.client,
		guildID: gid,
	}, nil
}

// AddEventHandler registers a handler for Discord events and returns a
// function that removes it. Safe for concurrent use; multiple handlers
// are supported for multi-bridge deployments.
func (dc *DisgoClient) AddEventHandler(handler EventHandler) func() {
	dc.mu.Lock()
	defer dc.mu.Unlock()

	dc.handlers = append(dc.handlers, handler)

	return func() {
		dc.mu.Lock()
		defer dc.mu.Unlock()
		for i, h := range dc.handlers {
			if h == handler {
				dc.handlers = append(dc.handlers[:i], dc.handlers[i+1:]...)
				return
			}
		}
	}
}

// getHandlers safely returns a snapshot of current event handlers.
func (dc *DisgoClient) getHandlers() []EventHandler {
	dc.mu.RLock()
	defer dc.mu.RUnlock()

	// Return a copy to avoid holding the lock during dispatch.
	out := make([]EventHandler, len(dc.handlers))
	copy(out, dc.handlers)
	return out
}

// Event handler callbacks

func (dc *DisgoClient) onReady(e *events.Ready) {
	dc.mu.Lock()
	dc.ready = true
	dc.botID = e.User.ID.String()
	dc.mu.Unlock()

	for _, h := range dc.getHandlers() {
		h.OnReady()
	}
}

func (dc *DisgoClient) onGuildAvailable(e *events.GuildAvailable) {
	handlers := dc.getHandlers()
	if len(handlers) == 0 {
		return
	}

	guild := &Guild{
		ID:   e.GuildID.String(),
		Name: e.Guild.Name,
	}

	for _, vs := range e.Guild.VoiceStates {
		channelID := ""
		if vs.ChannelID != nil {
			channelID = vs.ChannelID.String()
		}
		guild.VoiceStates = append(guild.VoiceStates, VoiceState{
			UserID:    vs.UserID.String(),
			ChannelID: channelID,
			GuildID:   e.GuildID.String(),
		})
	}

	for _, h := range handlers {
		h.OnGuildCreate(guild)
	}
}

func (dc *DisgoClient) onMessageCreate(e *events.MessageCreate) {
	handlers := dc.getHandlers()
	if len(handlers) == 0 {
		return
	}

	guildID := ""
	if e.GuildID != nil {
		guildID = e.GuildID.String()
	}

	msg := &Message{
		ID:        e.MessageID.String(),
		ChannelID: e.ChannelID.String(),
		GuildID:   guildID,
		Content:   e.Message.Content,
		Author: User{
			ID:       e.Message.Author.ID.String(),
			Username: e.Message.Author.Username,
			Bot:      e.Message.Author.Bot,
		},
	}

	for _, h := range handlers {
		h.OnMessageCreate(msg)
	}
}

// Voice state events — disgo splits these into Join/Move/Leave

func (dc *DisgoClient) onGuildVoiceJoin(e *events.GuildVoiceJoin) {
	dc.emitVoiceState(e.VoiceState)
}

func (dc *DisgoClient) onGuildVoiceMove(e *events.GuildVoiceMove) {
	dc.emitVoiceState(e.VoiceState)
}

func (dc *DisgoClient) onGuildVoiceLeave(e *events.GuildVoiceLeave) {
	handlers := dc.getHandlers()
	if len(handlers) == 0 {
		return
	}
	vs := &VoiceState{
		UserID:    e.VoiceState.UserID.String(),
		ChannelID: "",
		GuildID:   e.VoiceState.GuildID.String(),
	}
	for _, h := range handlers {
		h.OnVoiceStateUpdate(vs)
	}
}

func (dc *DisgoClient) emitVoiceState(vs discordmodel.VoiceState) {
	handlers := dc.getHandlers()
	if len(handlers) == 0 {
		return
	}

	channelID := ""
	if vs.ChannelID != nil {
		channelID = vs.ChannelID.String()
	}

	state := &VoiceState{
		UserID:    vs.UserID.String(),
		ChannelID: channelID,
		GuildID:   vs.GuildID.String(),
	}
	for _, h := range handlers {
		h.OnVoiceStateUpdate(state)
	}
}
