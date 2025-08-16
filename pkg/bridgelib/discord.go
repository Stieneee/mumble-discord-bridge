package bridgelib

import (
	"errors"
	"log"
	"strings"
	"sync"

	"github.com/bwmarrin/discordgo"
)

// SharedDiscordClient is a shared Discord client that can be used by multiple bridge instances
type SharedDiscordClient struct {
	// The Discord session
	session *discordgo.Session

	// Mapping of guild:channel to message handlers
	messageHandlers     map[string][]interface{}
	messageHandlerMutex sync.RWMutex

	// Mapping of guild to voice connections
	voiceConnections     map[string]*discordgo.VoiceConnection
	voiceConnectionMutex sync.RWMutex
}

// NewSharedDiscordClient creates a new shared Discord client
func NewSharedDiscordClient(token string) (*SharedDiscordClient, error) {
	// Create the Discord session
	session, err := discordgo.New("Bot " + token)
	if err != nil {
		return nil, err
	}

	// Set Discord library log level to only show errors
	session.LogLevel = discordgo.LogError

	// Set up the client
	client := &SharedDiscordClient{
		session:          session,
		messageHandlers:  make(map[string][]interface{}),
		voiceConnections: make(map[string]*discordgo.VoiceConnection),
	}

	// Set up intents
	intents := discordgo.MakeIntent(discordgo.IntentsGuildVoiceStates |
		discordgo.IntentsGuildMessages |
		discordgo.IntentsGuildMessageReactions |
		discordgo.IntentsDirectMessages |
		discordgo.IntentsDirectMessageReactions |
		discordgo.IntentsMessageContent)

	session.StateEnabled = true
	session.Identify.Intents = intents
	session.ShouldReconnectOnError = true

	// Register handlers for routing messages
	session.AddHandler(client.onMessageCreate)
	session.AddHandler(client.onVoiceStateUpdate)
	session.AddHandler(client.onGuildCreate)

	return client, nil
}

// Connect connects to Discord
func (c *SharedDiscordClient) Connect() error {
	return c.session.Open()
}

// Disconnect disconnects from Discord
func (c *SharedDiscordClient) Disconnect() error {
	// Close all voice connections
	c.voiceConnectionMutex.Lock()
	for _, vc := range c.voiceConnections {
		if err := vc.Disconnect(); err != nil {
			log.Printf("Error disconnecting voice connection: %v", err)
		}
	}
	c.voiceConnections = make(map[string]*discordgo.VoiceConnection)
	c.voiceConnectionMutex.Unlock()

	return c.session.Close()
}

// RegisterHandler registers a handler for Discord events
func (c *SharedDiscordClient) RegisterHandler(handlerFunc interface{}) {
	c.session.AddHandler(handlerFunc)
}

// JoinVoiceChannel joins a voice channel
func (c *SharedDiscordClient) JoinVoiceChannel(guildID, channelID string) (*discordgo.VoiceConnection, error) {
	key := guildID + ":" + channelID

	// Check if we already have a voice connection for this channel
	c.voiceConnectionMutex.RLock()
	vc, exists := c.voiceConnections[key]
	c.voiceConnectionMutex.RUnlock()

	if exists {
		return vc, nil
	}

	// Join the voice channel
	vc, err := c.session.ChannelVoiceJoin(guildID, channelID, false, true)
	if err != nil {
		return nil, err
	}

	// Store the voice connection
	c.voiceConnectionMutex.Lock()
	c.voiceConnections[key] = vc
	c.voiceConnectionMutex.Unlock()

	return vc, nil
}

// LeaveVoiceChannel leaves a voice channel
func (c *SharedDiscordClient) LeaveVoiceChannel(guildID, channelID string) error {
	key := guildID + ":" + channelID

	// Check if we have a voice connection for this channel
	c.voiceConnectionMutex.Lock()
	defer c.voiceConnectionMutex.Unlock()

	vc, exists := c.voiceConnections[key]
	if !exists {
		return errors.New("no voice connection for this channel")
	}

	// Disconnect from the voice channel
	err := vc.Disconnect()
	if err != nil {
		return err
	}

	// Remove the voice connection
	delete(c.voiceConnections, key)

	return nil
}

// SendMessage sends a message to a channel
func (c *SharedDiscordClient) SendMessage(channelID, content string) (*discordgo.Message, error) {
	return c.session.ChannelMessageSend(channelID, content)
}

// GetSession returns the underlying Discord session
func (c *SharedDiscordClient) GetSession() *discordgo.Session {
	return c.session
}

// RegisterMessageHandler registers a message handler for a specific guild and channel
func (c *SharedDiscordClient) RegisterMessageHandler(guildID, channelID string, handlerFunc interface{}) {
	key := guildID + ":" + channelID
	log.Printf("[DISCORD_CLIENT] Registering message handler for key: %s (handler type: %T)", key, handlerFunc)

	c.messageHandlerMutex.Lock()
	defer c.messageHandlerMutex.Unlock()

	if _, exists := c.messageHandlers[key]; !exists {
		log.Printf("[DISCORD_CLIENT] Creating new handler list for key: %s", key)
		c.messageHandlers[key] = make([]interface{}, 0)
	}

	c.messageHandlers[key] = append(c.messageHandlers[key], handlerFunc)
	log.Printf("[DISCORD_CLIENT] Handler registered. Total handlers for key %s: %d", key, len(c.messageHandlers[key]))
}

// UnregisterMessageHandler unregisters a message handler for a specific guild and channel
func (c *SharedDiscordClient) UnregisterMessageHandler(guildID, channelID string, handlerFunc interface{}) {
	key := guildID + ":" + channelID
	log.Printf("[DISCORD_CLIENT] Unregistering message handler for key: %s (handler type: %T)", key, handlerFunc)

	c.messageHandlerMutex.Lock()
	defer c.messageHandlerMutex.Unlock()

	handlers, exists := c.messageHandlers[key]
	if !exists {
		log.Printf("[DISCORD_CLIENT] No handlers found for key: %s", key)
		return
	}

	log.Printf("[DISCORD_CLIENT] Found %d handlers for key: %s", len(handlers), key)

	// Find and remove the handler
	found := false
	for i, h := range handlers {
		if h == handlerFunc {
			log.Printf("[DISCORD_CLIENT] Found handler at index %d, removing it", i)
			c.messageHandlers[key] = append(handlers[:i], handlers[i+1:]...)
			found = true
			break
		}
	}

	if !found {
		log.Printf("[DISCORD_CLIENT] Handler not found for key: %s", key)
	}

	// Remove the key if there are no more handlers
	if len(c.messageHandlers[key]) == 0 {
		log.Printf("[DISCORD_CLIENT] No more handlers for key: %s, removing key", key)
		delete(c.messageHandlers, key)
	} else {
		log.Printf("[DISCORD_CLIENT] %d handlers remaining for key: %s", len(c.messageHandlers[key]), key)
	}
}

// onMessageCreate routes message create events to the appropriate handlers
func (c *SharedDiscordClient) onMessageCreate(s *discordgo.Session, m *discordgo.MessageCreate) {
	// Log truncated message content to avoid flooding logs
	content := m.Content
	if len(content) > 50 {
		content = content[:47] + "..."
	}
	log.Printf("[DISCORD_CLIENT] Message received from %s: %s", m.Author.Username, content)

	// Skip messages from the bot itself
	if m.Author.ID == s.State.User.ID {
		log.Printf("[DISCORD_CLIENT] Skipping message from self")
		return
	}

	// Get the key for this message
	key := m.GuildID + ":" + m.ChannelID
	log.Printf("[DISCORD_CLIENT] Message key: %s (GuildID: %s, ChannelID: %s)", key, m.GuildID, m.ChannelID)

	// Get the handlers for this key
	c.messageHandlerMutex.RLock()
	handlers, exists := c.messageHandlers[key]
	totalHandlers := len(c.messageHandlers)

	// Only log handler details in verbose mode
	if totalHandlers < 5 {
		// Debug: list all registered handlers
		log.Printf("[DISCORD_CLIENT] Total registered handler keys: %d", totalHandlers)
		for k, hdlrs := range c.messageHandlers {
			log.Printf("[DISCORD_CLIENT] Registered key: %s with %d handlers", k, len(hdlrs))
		}
	} else {
		log.Printf("[DISCORD_CLIENT] Total registered handler keys: %d", totalHandlers)
	}
	c.messageHandlerMutex.RUnlock()

	if !exists {
		log.Printf("[DISCORD_CLIENT] No specific handlers found for key %s (checking global handlers)", key)

		// Try global handler as a fallback
		c.messageHandlerMutex.RLock()
		defer c.messageHandlerMutex.RUnlock()

		// If no specific handler is found, look for handlers that might apply to this guild generally
		foundGlobalHandlers := false

		// Try to find handlers with just the guild ID as a prefix
		guildPrefix := m.GuildID + ":"
		for k, hdlrs := range c.messageHandlers {
			if strings.HasPrefix(k, guildPrefix) {
				log.Printf("[DISCORD_CLIENT] Found handlers for guild prefix %s: %s with %d handlers",
					guildPrefix, k, len(hdlrs))

				// Call all handlers for this guild prefix
				handlersCalled := 0
				for i, h := range hdlrs {
					if handler, ok := h.(func(*discordgo.Session, *discordgo.MessageCreate)); ok {
						log.Printf("[DISCORD_CLIENT] Calling guild-level handler #%d for message", i+1)
						handler(s, m)
						handlersCalled++
						foundGlobalHandlers = true
					}
				}

				log.Printf("[DISCORD_CLIENT] Called %d guild-level handlers for message", handlersCalled)
			}
		}

		if !foundGlobalHandlers {
			log.Printf("[DISCORD_CLIENT] No handlers found for this message (guild ID: %s, channel ID: %s)",
				m.GuildID, m.ChannelID)
		}

		return
	}

	log.Printf("[DISCORD_CLIENT] Found %d specific handlers for key %s", len(handlers), key)

	// Call all channel-specific handlers
	handlersCalled := 0
	handlerErrors := 0

	for i, h := range handlers {
		if handler, ok := h.(func(*discordgo.Session, *discordgo.MessageCreate)); ok {
			log.Printf("[DISCORD_CLIENT] Calling channel-specific handler #%d for message", i+1)

			// Execute the handler in a defensive way
			func() {
				defer func() {
					if r := recover(); r != nil {
						log.Printf("[DISCORD_CLIENT] ERROR: Handler #%d panicked: %v", i+1, r)
						handlerErrors++
					}
				}()

				handler(s, m)
				handlersCalled++
				log.Printf("[DISCORD_CLIENT] Handler #%d executed successfully", i+1)
			}()
		} else {
			log.Printf("[DISCORD_CLIENT] Handler #%d is not a MessageCreate handler, type: %T", i+1, h)
		}
	}

	log.Printf("[DISCORD_CLIENT] Successfully called %d handlers for message (errors: %d)",
		handlersCalled, handlerErrors)
}

// onVoiceStateUpdate routes voice state update events to the appropriate handlers
func (c *SharedDiscordClient) onVoiceStateUpdate(s *discordgo.Session, v *discordgo.VoiceStateUpdate) {
	log.Printf("[DISCORD_CLIENT] Voice state update for user ID: %s in guild: %s, channel: %s",
		v.UserID, v.GuildID, v.ChannelID)

	// If channel is empty, this is likely a user disconnecting
	if v.ChannelID == "" {
		log.Printf("[DISCORD_CLIENT] User %s disconnected from voice", v.UserID)
		return
	}

	// Get the key for this voice state
	key := v.GuildID + ":" + v.ChannelID
	log.Printf("[DISCORD_CLIENT] Voice state key: %s", key)

	// Get the handlers for this key
	c.messageHandlerMutex.RLock()
	handlers, exists := c.messageHandlers[key]
	c.messageHandlerMutex.RUnlock()

	if !exists {
		log.Printf("[DISCORD_CLIENT] No voice state handlers found for key: %s", key)
		return
	}

	log.Printf("[DISCORD_CLIENT] Found %d voice state handlers for key: %s", len(handlers), key)

	// Call all handlers
	handlersCalled := 0
	for i, h := range handlers {
		if handler, ok := h.(func(*discordgo.Session, *discordgo.VoiceStateUpdate)); ok {
			log.Printf("[DISCORD_CLIENT] Calling voice state handler #%d", i+1)
			handler(s, v)
			handlersCalled++
		}
	}

	log.Printf("[DISCORD_CLIENT] Called %d voice state handlers", handlersCalled)
}

// onGuildCreate routes guild create events to the appropriate handlers
func (c *SharedDiscordClient) onGuildCreate(s *discordgo.Session, g *discordgo.GuildCreate) {
	log.Printf("[DISCORD_CLIENT] Guild create event for guild: %s (%s) with %d channels",
		g.Name, g.ID, len(g.Channels))

	// Route to all handlers for this guild
	prefix := g.ID + ":"
	log.Printf("[DISCORD_CLIENT] Looking for handlers with prefix: %s", prefix)

	// Get all handlers for this guild
	c.messageHandlerMutex.RLock()
	defer c.messageHandlerMutex.RUnlock()

	totalHandlers := 0
	handlersCalled := 0

	for key, handlers := range c.messageHandlers {
		if len(key) >= len(prefix) && key[:len(prefix)] == prefix {
			log.Printf("[DISCORD_CLIENT] Found matching key: %s with %d handlers", key, len(handlers))
			totalHandlers += len(handlers)

			// Call all handlers
			for i, h := range handlers {
				if handler, ok := h.(func(*discordgo.Session, *discordgo.GuildCreate)); ok {
					log.Printf("[DISCORD_CLIENT] Calling guild create handler #%d for key: %s", i+1, key)
					handler(s, g)
					handlersCalled++
				} else {
					log.Printf("[DISCORD_CLIENT] Handler #%d for key %s is not a GuildCreate handler, type: %T",
						i+1, key, h)
				}
			}
		}
	}

	if totalHandlers == 0 {
		log.Printf("[DISCORD_CLIENT] No handlers found for guild: %s", g.ID)
	} else {
		log.Printf("[DISCORD_CLIENT] Called %d out of %d handlers for guild: %s",
			handlersCalled, totalHandlers, g.ID)
	}
}
