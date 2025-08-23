package bridgelib

import (
	"fmt"
	"strings"
	"sync"

	"github.com/bwmarrin/discordgo"
)

// SharedDiscordClient is a shared Discord client that can be used by multiple bridge instances
type SharedDiscordClient struct {
	// The Discord session
	session *discordgo.Session

	// Logger for the Discord client
	logger Logger

	// Mapping of guild:channel to message handlers
	messageHandlers     map[string][]interface{}
	messageHandlerMutex sync.RWMutex
}

// NewSharedDiscordClient creates a new shared Discord client
func NewSharedDiscordClient(token string, logger Logger) (*SharedDiscordClient, error) {
	// Use provided logger or create a default console logger
	if logger == nil {
		logger = NewConsoleLogger()
	}

	logger.Debug("DISCORD_CLIENT", "Starting Discord client creation")

	// Create the Discord session
	logger.Debug("DISCORD_CLIENT", "Creating Discord session")
	session, err := discordgo.New("Bot " + token)
	if err != nil {
		logger.Error("DISCORD_CLIENT", fmt.Sprintf("Failed to create Discord session: %v", err))
		return nil, err
	}
	logger.Debug("DISCORD_CLIENT", "Discord session created successfully")

	// Set Discord library log level to only show errors
	logger.Debug("DISCORD_CLIENT", "Setting Discord library log level to ERROR")
	session.LogLevel = discordgo.LogError

	// Set up the client
	logger.Debug("DISCORD_CLIENT", "Creating SharedDiscordClient struct")
	client := &SharedDiscordClient{
		session:         session,
		logger:          logger,
		messageHandlers: make(map[string][]interface{}),
	}
	logger.Debug("DISCORD_CLIENT", "SharedDiscordClient struct created")

	// Set up intents
	logger.Debug("DISCORD_CLIENT", "Setting up Discord intents")
	intents := discordgo.MakeIntent(discordgo.IntentsGuildMessages |
		discordgo.IntentsGuildMessageReactions |
		discordgo.IntentsDirectMessages |
		discordgo.IntentsDirectMessageReactions |
		discordgo.IntentsMessageContent |
		discordgo.IntentsGuildVoiceStates)
	logger.Debug("DISCORD_CLIENT", fmt.Sprintf("Discord intents configured: %d", intents))

	logger.Debug("DISCORD_CLIENT", "Configuring Discord session settings")
	session.StateEnabled = true
	session.Identify.Intents = intents
	session.ShouldReconnectOnError = true
	logger.Debug("DISCORD_CLIENT", "Discord session settings configured")

	// Register handlers for routing messages
	logger.Debug("DISCORD_CLIENT", "Registering Discord event handlers")
	session.AddHandler(client.onMessageCreate)
	session.AddHandler(client.onGuildCreate)
	logger.Debug("DISCORD_CLIENT", "Discord event handlers registered")

	logger.Info("DISCORD_CLIENT", "SharedDiscordClient created successfully")
	return client, nil
}

// Connect connects to Discord
func (c *SharedDiscordClient) Connect() error {
	c.logger.Info("DISCORD_CLIENT", "Connecting to Discord")
	c.logger.Debug("DISCORD_CLIENT", "Calling session.Open()")
	err := c.session.Open()
	if err != nil {
		c.logger.Error("DISCORD_CLIENT", fmt.Sprintf("Failed to connect to Discord: %v", err))
		return err
	}
	c.logger.Info("DISCORD_CLIENT", "Successfully connected to Discord")
	return nil
}

// Disconnect disconnects from Discord
func (c *SharedDiscordClient) Disconnect() error {
	c.logger.Info("DISCORD_CLIENT", "Disconnecting from Discord")
	c.logger.Debug("DISCORD_CLIENT", "Closing Discord session")
	err := c.session.Close()
	if err != nil {
		c.logger.Error("DISCORD_CLIENT", fmt.Sprintf("Error closing Discord session: %v", err))
		return err
	}
	c.logger.Info("DISCORD_CLIENT", "Successfully disconnected from Discord")
	return nil
}

// RegisterHandler registers a handler for Discord events
func (c *SharedDiscordClient) RegisterHandler(handlerFunc interface{}) {
	c.session.AddHandler(handlerFunc)
}

// SendMessage sends a message to a channel
func (c *SharedDiscordClient) SendMessage(channelID, content string) (*discordgo.Message, error) {
	// Truncate content for logging if it's too long
	logContent := content
	if len(logContent) > 100 {
		logContent = logContent[:97] + "..."
	}
	c.logger.Debug("DISCORD_CLIENT", fmt.Sprintf("Sending message to channel %s: %s", channelID, logContent))

	msg, err := c.session.ChannelMessageSend(channelID, content)
	if err != nil {
		c.logger.Error("DISCORD_CLIENT", fmt.Sprintf("Failed to send message to channel %s: %v", channelID, err))
		return nil, err
	}

	c.logger.Debug("DISCORD_CLIENT", fmt.Sprintf("Message sent successfully to channel %s", channelID))
	return msg, nil
}

// GetSession returns the underlying Discord session
func (c *SharedDiscordClient) GetSession() *discordgo.Session {
	return c.session
}

// RegisterMessageHandler registers a message handler for a specific guild and channel
func (c *SharedDiscordClient) RegisterMessageHandler(guildID, channelID string, handlerFunc interface{}) {
	key := guildID + ":" + channelID
	c.logger.Debug("DISCORD_CLIENT", fmt.Sprintf("Registering message handler for key: %s (handler type: %T)", key, handlerFunc))

	c.messageHandlerMutex.Lock()
	defer c.messageHandlerMutex.Unlock()

	if _, exists := c.messageHandlers[key]; !exists {
		c.logger.Debug("DISCORD_CLIENT", fmt.Sprintf("Creating new handler list for key: %s", key))
		c.messageHandlers[key] = make([]interface{}, 0)
	}

	c.messageHandlers[key] = append(c.messageHandlers[key], handlerFunc)
	c.logger.Debug("DISCORD_CLIENT", fmt.Sprintf("Handler registered. Total handlers for key %s: %d", key, len(c.messageHandlers[key])))
}

// UnregisterMessageHandler unregisters a message handler for a specific guild and channel
// Note: Since function pointers can't be compared directly, this clears all handlers for the key
func (c *SharedDiscordClient) UnregisterMessageHandler(guildID, channelID string, handlerFunc interface{}) {
	key := guildID + ":" + channelID
	c.logger.Debug("DISCORD_CLIENT", fmt.Sprintf("Clearing all message handlers for key: %s", key))

	c.messageHandlerMutex.Lock()
	defer c.messageHandlerMutex.Unlock()

	handlers, exists := c.messageHandlers[key]
	if !exists {
		c.logger.Debug("DISCORD_CLIENT", fmt.Sprintf("No handlers found for key: %s", key))
		return
	}

	c.logger.Debug("DISCORD_CLIENT", fmt.Sprintf("Removing %d handlers for key: %s", len(handlers), key))

	// Remove all handlers for this key since we can't compare function pointers
	delete(c.messageHandlers, key)
	c.logger.Debug("DISCORD_CLIENT", fmt.Sprintf("All handlers cleared for key: %s", key))
}

// onMessageCreate routes message create events to the appropriate handlers
func (c *SharedDiscordClient) onMessageCreate(s *discordgo.Session, m *discordgo.MessageCreate) {
	// Log truncated message content to avoid flooding logs
	content := m.Content
	if len(content) > 50 {
		content = content[:47] + "..."
	}
	c.logger.Debug("DISCORD_HANDLER", fmt.Sprintf("Message received from %s: %s", m.Author.Username, content))

	// Skip messages from the bot itself
	if m.Author.ID == s.State.User.ID {
		c.logger.Debug("DISCORD_HANDLER", "Skipping message from self")
		return
	}

	// Get the key for this message
	key := m.GuildID + ":" + m.ChannelID
	c.logger.Debug("DISCORD_HANDLER", fmt.Sprintf("Message key: %s (GuildID: %s, ChannelID: %s)", key, m.GuildID, m.ChannelID))

	// Get the handlers for this key
	c.messageHandlerMutex.RLock()
	handlers, exists := c.messageHandlers[key]
	totalHandlers := len(c.messageHandlers)

	// Only log handler details in verbose mode
	if totalHandlers < 5 {
		// Debug: list all registered handlers
		c.logger.Debug("DISCORD_HANDLER", fmt.Sprintf("Total registered handler keys: %d", totalHandlers))
		for k, hdlrs := range c.messageHandlers {
			c.logger.Debug("DISCORD_HANDLER", fmt.Sprintf("Registered key: %s with %d handlers", k, len(hdlrs)))
		}
	} else {
		c.logger.Debug("DISCORD_HANDLER", fmt.Sprintf("Total registered handler keys: %d", totalHandlers))
	}
	c.messageHandlerMutex.RUnlock()

	if !exists {
		c.logger.Debug("DISCORD_HANDLER", fmt.Sprintf("No specific handlers found for key %s (checking global handlers)", key))

		// Try global handler as a fallback
		c.messageHandlerMutex.RLock()
		defer c.messageHandlerMutex.RUnlock()

		// If no specific handler is found, look for handlers that might apply to this guild generally
		foundGlobalHandlers := false

		// Try to find handlers with just the guild ID as a prefix
		guildPrefix := m.GuildID + ":"
		for k, hdlrs := range c.messageHandlers {
			if strings.HasPrefix(k, guildPrefix) {
				c.logger.Debug("DISCORD_HANDLER", fmt.Sprintf("Found handlers for guild prefix %s: %s with %d handlers", guildPrefix, k, len(hdlrs)))

				// Call all handlers for this guild prefix
				handlersCalled := 0
				for i, h := range hdlrs {
					if handler, ok := h.(func(*discordgo.Session, *discordgo.MessageCreate)); ok {
						c.logger.Debug("DISCORD_HANDLER", fmt.Sprintf("Calling guild-level handler #%d for message", i+1))
						handler(s, m)
						handlersCalled++
						foundGlobalHandlers = true
					}
				}

				c.logger.Debug("DISCORD_HANDLER", fmt.Sprintf("Called %d guild-level handlers for message", handlersCalled))
			}
		}

		if !foundGlobalHandlers {
			c.logger.Debug("DISCORD_HANDLER", fmt.Sprintf("No handlers found for this message (guild ID: %s, channel ID: %s)", m.GuildID, m.ChannelID))
		}

		return
	}

	c.logger.Debug("DISCORD_HANDLER", fmt.Sprintf("Found %d specific handlers for key %s", len(handlers), key))

	// Call all channel-specific handlers
	handlersCalled := 0
	handlerErrors := 0

	for i, h := range handlers {
		if handler, ok := h.(func(*discordgo.Session, *discordgo.MessageCreate)); ok {
			c.logger.Debug("DISCORD_HANDLER", fmt.Sprintf("Calling channel-specific handler #%d for message", i+1))

			// Execute the handler in a defensive way
			func() {
				defer func() {
					if r := recover(); r != nil {
						c.logger.Error("DISCORD_HANDLER", fmt.Sprintf("Handler #%d panicked: %v", i+1, r))
						handlerErrors++
					}
				}()

				handler(s, m)
				handlersCalled++
				c.logger.Debug("DISCORD_HANDLER", fmt.Sprintf("Handler #%d executed successfully", i+1))
			}()
		} else {
			c.logger.Debug("DISCORD_HANDLER", fmt.Sprintf("Handler #%d is not a MessageCreate handler, type: %T", i+1, h))
		}
	}

	c.logger.Info("DISCORD_HANDLER", fmt.Sprintf("Successfully called %d handlers for message (errors: %d)", handlersCalled, handlerErrors))
}

// onGuildCreate routes guild create events to the appropriate handlers
func (c *SharedDiscordClient) onGuildCreate(s *discordgo.Session, g *discordgo.GuildCreate) {
	c.logger.Info("DISCORD_HANDLER", fmt.Sprintf("Guild create event for guild: %s (%s) with %d channels", g.Name, g.ID, len(g.Channels)))

	// Route to all handlers for this guild
	prefix := g.ID + ":"
	c.logger.Debug("DISCORD_HANDLER", fmt.Sprintf("Looking for handlers with prefix: %s", prefix))

	// Get all handlers for this guild
	c.messageHandlerMutex.RLock()
	defer c.messageHandlerMutex.RUnlock()

	totalHandlers := 0
	handlersCalled := 0

	for key, handlers := range c.messageHandlers {
		if len(key) >= len(prefix) && key[:len(prefix)] == prefix {
			c.logger.Debug("DISCORD_HANDLER", fmt.Sprintf("Found matching key: %s with %d handlers", key, len(handlers)))
			totalHandlers += len(handlers)

			// Call all handlers
			for i, h := range handlers {
				if handler, ok := h.(func(*discordgo.Session, *discordgo.GuildCreate)); ok {
					c.logger.Debug("DISCORD_HANDLER", fmt.Sprintf("Calling guild create handler #%d for key: %s", i+1, key))
					handler(s, g)
					handlersCalled++
				} else {
					c.logger.Debug("DISCORD_HANDLER", fmt.Sprintf("Handler #%d for key %s is not a GuildCreate handler, type: %T", i+1, key, h))
				}
			}
		}
	}

	if totalHandlers == 0 {
		c.logger.Debug("DISCORD_HANDLER", fmt.Sprintf("No handlers found for guild: %s", g.ID))
	} else {
		c.logger.Info("DISCORD_HANDLER", fmt.Sprintf("Called %d out of %d handlers for guild: %s", handlersCalled, totalHandlers, g.ID))
	}
}
