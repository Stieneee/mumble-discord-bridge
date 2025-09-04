package bridgelib

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/stieneee/mumble-discord-bridge/pkg/logger"
)

// SharedDiscordClient is a shared Discord client that can be used by multiple bridge instances
type SharedDiscordClient struct {
	// The Discord session
	session *discordgo.Session

	// Logger for the Discord client
	logger logger.Logger

	// Mapping of guild:channel to message handlers
	messageHandlers     map[string][]interface{}
	messageHandlerMutex sync.RWMutex

	// Session monitoring and reconnection
	ctx                 context.Context
	cancel              context.CancelFunc
	monitoringEnabled   bool
	monitoringMutex     sync.RWMutex
	reconnectInProgress bool
	reconnectMutex      sync.Mutex
}

// NewSharedDiscordClient creates a new shared Discord client
func NewSharedDiscordClient(token string, lgr logger.Logger) (*SharedDiscordClient, error) {
	// Use provided logger or create a default console logger
	if lgr == nil {
		lgr = logger.NewConsoleLogger()
	}

	lgr.Debug("DISCORD_CLIENT", "Starting Discord client creation")

	// Create the Discord session
	lgr.Debug("DISCORD_CLIENT", "Creating Discord session")
	session, err := discordgo.New("Bot " + token)
	if err != nil {
		lgr.Error("DISCORD_CLIENT", fmt.Sprintf("Failed to create Discord session: %v", err))

		return nil, err
	}
	lgr.Debug("DISCORD_CLIENT", "Discord session created successfully")

	// Set Discord library log level to only show errors
	lgr.Debug("DISCORD_CLIENT", "Setting Discord library log level to ERROR")
	session.LogLevel = discordgo.LogError

	// Set up the client
	lgr.Debug("DISCORD_CLIENT", "Creating SharedDiscordClient struct")
	ctx, cancel := context.WithCancel(context.Background())
	client := &SharedDiscordClient{
		session:         session,
		logger:          lgr,
		messageHandlers: make(map[string][]interface{}),
		ctx:             ctx,
		cancel:          cancel,
	}
	lgr.Debug("DISCORD_CLIENT", "SharedDiscordClient struct created")

	// Set up intents
	lgr.Debug("DISCORD_CLIENT", "Setting up Discord intents")
	intents := discordgo.MakeIntent(discordgo.IntentsGuildMessages |
		discordgo.IntentsGuildMessageReactions |
		discordgo.IntentsDirectMessages |
		discordgo.IntentsDirectMessageReactions |
		discordgo.IntentsMessageContent |
		discordgo.IntentsGuildVoiceStates)
	lgr.Debug("DISCORD_CLIENT", fmt.Sprintf("Discord intents configured: %d", intents))

	lgr.Debug("DISCORD_CLIENT", "Configuring Discord session settings")
	session.StateEnabled = true
	session.Identify.Intents = intents
	session.ShouldReconnectOnError = false
	session.ShouldReconnectVoiceOnSessionError = false // discord_connection_manager handles voice reconnection
	session.ShouldRetryOnRateLimit = true
	lgr.Debug("DISCORD_CLIENT", "Discord session settings configured")

	// Register handlers for routing messages
	lgr.Debug("DISCORD_CLIENT", "Registering Discord event handlers")
	session.AddHandler(client.onMessageCreate)
	session.AddHandler(client.onGuildCreate)
	lgr.Debug("DISCORD_CLIENT", "Discord event handlers registered")

	lgr.Info("DISCORD_CLIENT", "SharedDiscordClient created successfully")

	return client, nil
}

// Connect connects to Discord and starts session monitoring
func (c *SharedDiscordClient) Connect() error {
	c.logger.Info("DISCORD_CLIENT", "Connecting to Discord")
	c.logger.Debug("DISCORD_CLIENT", "Calling session.Open()")
	err := c.session.Open()
	if err != nil {
		c.logger.Error("DISCORD_CLIENT", fmt.Sprintf("Failed to connect to Discord: %v", err))

		return err
	}
	c.logger.Info("DISCORD_CLIENT", "Successfully connected to Discord")

	// Start session monitoring
	c.startSessionMonitoring()

	return nil
}

// Disconnect disconnects from Discord and stops session monitoring
func (c *SharedDiscordClient) Disconnect() error {
	c.logger.Info("DISCORD_CLIENT", "Disconnecting from Discord")

	// Stop session monitoring
	c.stopSessionMonitoring()

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
func (c *SharedDiscordClient) UnregisterMessageHandler(guildID, channelID string, _ interface{}) {
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

// startSessionMonitoring starts the session health monitoring goroutine
func (c *SharedDiscordClient) startSessionMonitoring() {
	c.monitoringMutex.Lock()
	defer c.monitoringMutex.Unlock()

	if c.monitoringEnabled {
		c.logger.Debug("DISCORD_CLIENT", "Session monitoring already enabled")
		return
	}

	c.monitoringEnabled = true
	c.logger.Info("DISCORD_CLIENT", "Starting Discord session health monitoring")

	go c.sessionMonitorLoop()
}

// stopSessionMonitoring stops the session health monitoring
func (c *SharedDiscordClient) stopSessionMonitoring() {
	c.monitoringMutex.Lock()
	defer c.monitoringMutex.Unlock()

	if !c.monitoringEnabled {
		c.logger.Debug("DISCORD_CLIENT", "Session monitoring not enabled")
		return
	}

	c.logger.Info("DISCORD_CLIENT", "Stopping Discord session health monitoring")
	c.monitoringEnabled = false
	c.cancel()
}

// sessionMonitorLoop monitors the session health and handles reconnection
func (c *SharedDiscordClient) sessionMonitorLoop() {
	c.logger.Info("DISCORD_CLIENT", "Session monitoring loop started")
	ticker := time.NewTicker(5 * time.Second) // Check every 5 seconds
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			c.logger.Info("DISCORD_CLIENT", "Session monitoring loop exiting")
			return
		case <-ticker.C:
			if !c.isSessionHealthy() {
				c.logger.Warn("DISCORD_CLIENT", "Discord session unhealthy, attempting indefinite reconnection")
				c.attemptIndefiniteReconnection()
			}
		}
	}
}

// isSessionHealthy checks if the Discord session is healthy and ready
func (c *SharedDiscordClient) isSessionHealthy() bool {
	if c.session == nil {
		c.logger.Debug("DISCORD_CLIENT", "Session health check: session is nil")
		return false
	}

	// Check if session is properly connected
	c.session.RLock()
	defer c.session.RUnlock()

	// Check DataReady flag - this indicates the session is fully initialized
	if !c.session.DataReady {
		c.logger.Debug("DISCORD_CLIENT", "Session health check: DataReady is false")
		return false
	}

	// Check if we have user state - indicates successful authentication
	if c.session.State == nil || c.session.State.User == nil {
		c.logger.Debug("DISCORD_CLIENT", "Session health check: no user state available")
		return false
	}

	return true
}

// attemptIndefiniteReconnection attempts to reconnect the Discord session indefinitely with fixed delay
func (c *SharedDiscordClient) attemptIndefiniteReconnection() {
	c.reconnectMutex.Lock()
	defer c.reconnectMutex.Unlock()

	// Prevent multiple concurrent reconnection attempts
	if c.reconnectInProgress {
		c.logger.Debug("DISCORD_CLIENT", "Reconnection already in progress, skipping")
		return
	}

	c.reconnectInProgress = true
	defer func() {
		c.reconnectInProgress = false
	}()

	c.logger.Info("DISCORD_CLIENT", "Starting indefinite Discord session reconnection")

	fixedDelay := 10 * time.Second // Fixed 10-second delay between attempts
	attempt := 1

	for {
		select {
		case <-c.ctx.Done():
			c.logger.Info("DISCORD_CLIENT", "Reconnection canceled due to context cancellation")
			return
		default:
			c.logger.Info("DISCORD_CLIENT", fmt.Sprintf("Reconnection attempt #%d", attempt))

			// First, try to close the existing session
			c.logger.Debug("DISCORD_CLIENT", "Closing existing session before reconnection")
			if err := c.session.Close(); err != nil {
				c.logger.Warn("DISCORD_CLIENT", fmt.Sprintf("Error closing existing session: %v", err))
			}

			// Wait before reopening
			c.logger.Debug("DISCORD_CLIENT", fmt.Sprintf("Waiting %v before reconnection attempt", fixedDelay))
			select {
			case <-c.ctx.Done():
				c.logger.Info("DISCORD_CLIENT", "Reconnection canceled during delay")
				return
			case <-time.After(fixedDelay):
				// Continue with reconnection attempt
			}

			// Attempt to reopen the session
			c.logger.Debug("DISCORD_CLIENT", "Attempting to reopen Discord session")
			if err := c.session.Open(); err != nil {
				c.logger.Error("DISCORD_CLIENT", fmt.Sprintf("Reconnection attempt #%d failed: %v", attempt, err))
				attempt++
				continue
			}

			// Wait for the session to become ready
			c.logger.Debug("DISCORD_CLIENT", "Waiting for session to become ready after reconnection")
			if c.waitForSessionReady(30 * time.Second) {
				c.logger.Info("DISCORD_CLIENT", fmt.Sprintf("Discord session reconnection successful after %d attempts", attempt))
				return
			}

			c.logger.Warn("DISCORD_CLIENT", fmt.Sprintf("Session not ready after reconnection attempt #%d", attempt))
			attempt++
		}
	}
}

// waitForSessionReady waits for the session to become ready
func (c *SharedDiscordClient) waitForSessionReady(timeout time.Duration) bool {
	start := time.Now()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		if c.isSessionHealthy() {
			c.logger.Debug("DISCORD_CLIENT", "Session became ready")
			return true
		}

		if time.Since(start) > timeout {
			c.logger.Warn("DISCORD_CLIENT", "Timeout waiting for session to become ready")
			return false
		}

		select {
		case <-ticker.C:
			// Continue checking
		case <-c.ctx.Done():
			c.logger.Debug("DISCORD_CLIENT", "Context canceled while waiting for session ready")
			return false
		}
	}
}

// IsSessionHealthy exposes session health check for external callers
func (c *SharedDiscordClient) IsSessionHealthy() bool {
	return c.isSessionHealthy()
}
