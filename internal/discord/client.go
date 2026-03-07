package discord

import "context"

// Client defines the interface for a Discord bot client.
type Client interface {
	// Connect opens the gateway connection.
	Connect(ctx context.Context) error
	// Disconnect closes the gateway and all voice connections.
	Disconnect(ctx context.Context) error
	// SendMessage sends a text message to a channel.
	SendMessage(channelID, content string) error
	// GetUser retrieves a user by ID.
	GetUser(userID string) (*User, error)
	// CreateDM creates a DM channel and returns its ID.
	CreateDM(userID string) (string, error)
	// GetGuild retrieves guild information including voice states.
	GetGuild(guildID string) (*Guild, error)
	// GetBotUserID returns the bot's own user ID.
	GetBotUserID() string
	// IsReady returns true if the gateway is connected and ready.
	IsReady() bool
	// CreateVoiceConnection creates a new VoiceConnection for the given guild.
	CreateVoiceConnection(guildID string) (VoiceConnection, error)
	// AddEventHandler registers a handler for Discord events and returns a
	// function that removes it. Multiple handlers may be registered; each
	// receives every event.
	AddEventHandler(handler EventHandler) func()
}
