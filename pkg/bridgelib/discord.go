package bridgelib

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/stieneee/mumble-discord-bridge/internal/discord"
	"github.com/stieneee/mumble-discord-bridge/pkg/logger"
)

// SharedDiscordClient is a shared Discord client that can be used by multiple bridge instances.
// It wraps discord.Client (backed by disgo+godave with DAVE E2EE).
type SharedDiscordClient struct {
	// The Discord client (disgo+godave with DAVE E2EE)
	client discord.Client

	// Logger for the Discord client
	logger logger.Logger

	// Session monitoring
	ctx               context.Context
	cancel            context.CancelFunc
	monitoringEnabled bool
	monitoringMutex   sync.RWMutex
}

// NewSharedDiscordClient creates a new shared Discord client
func NewSharedDiscordClient(token string, lgr logger.Logger) (*SharedDiscordClient, error) {
	// Use provided logger or create a default console logger
	if lgr == nil {
		lgr = logger.NewConsoleLogger()
	}

	lgr.Debug("DISCORD_CLIENT", "Starting Discord client creation with disgo+godave (DAVE E2EE)")

	client, err := discord.NewDisgoClient(token)
	if err != nil {
		lgr.Error("DISCORD_CLIENT", fmt.Sprintf("Failed to create Discord client: %v", err))

		return nil, err
	}
	lgr.Debug("DISCORD_CLIENT", "Discord client created successfully")

	ctx, cancel := context.WithCancel(context.Background())
	shared := &SharedDiscordClient{
		client: client,
		logger: lgr,
		ctx:    ctx,
		cancel: cancel,
	}

	lgr.Info("DISCORD_CLIENT", "SharedDiscordClient created successfully with DAVE E2EE support")

	return shared, nil
}

// Connect connects to Discord and starts session monitoring
func (c *SharedDiscordClient) Connect() error {
	c.logger.Info("DISCORD_CLIENT", "Connecting to Discord via disgo gateway")
	err := c.client.Connect(context.Background())
	if err != nil {
		c.logger.Error("DISCORD_CLIENT", fmt.Sprintf("Failed to connect to Discord: %v", err))

		return err
	}
	c.logger.Info("DISCORD_CLIENT", "Successfully connected to Discord")

	// Start session monitoring
	c.startSessionMonitoring()

	return nil
}

// Disconnect disconnects from Discord
func (c *SharedDiscordClient) Disconnect() error {
	c.logger.Info("DISCORD_CLIENT", "Disconnecting from Discord")

	// Stop session monitoring
	c.stopSessionMonitoring()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := c.client.Disconnect(ctx)
	if err != nil {
		c.logger.Error("DISCORD_CLIENT", fmt.Sprintf("Error disconnecting: %v", err))

		return err
	}
	c.logger.Info("DISCORD_CLIENT", "Successfully disconnected from Discord")

	return nil
}

// GetClient returns the underlying discord.Client
func (c *SharedDiscordClient) GetClient() discord.Client {
	return c.client
}

// SendMessage sends a message to a channel
func (c *SharedDiscordClient) SendMessage(channelID, content string) error {
	// Truncate content for logging if it's too long
	logContent := content
	if len(logContent) > 100 {
		logContent = logContent[:97] + "..."
	}
	c.logger.Debug("DISCORD_CLIENT", fmt.Sprintf("Sending message to channel %s: %s", channelID, logContent))

	err := c.client.SendMessage(channelID, content)
	if err != nil {
		c.logger.Error("DISCORD_CLIENT", fmt.Sprintf("Failed to send message to channel %s: %v", channelID, err))

		return err
	}

	c.logger.Debug("DISCORD_CLIENT", fmt.Sprintf("Message sent successfully to channel %s", channelID))

	return nil
}

// IsSessionHealthy checks if the Discord session is healthy
func (c *SharedDiscordClient) IsSessionHealthy() bool {
	return c.client.IsReady()
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

// sessionMonitorLoop monitors session health
func (c *SharedDiscordClient) sessionMonitorLoop() {
	c.logger.Info("DISCORD_CLIENT", "Session monitoring loop started")
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	var unhealthySince time.Time

	for {
		select {
		case <-c.ctx.Done():
			c.logger.Info("DISCORD_CLIENT", "Session monitoring loop exiting")

			return
		case <-ticker.C:
			if c.client.IsReady() {
				if !unhealthySince.IsZero() {
					duration := time.Since(unhealthySince)
					c.logger.Info("DISCORD_CLIENT", fmt.Sprintf("Discord session recovered after %v", duration))
					unhealthySince = time.Time{}
				}
			} else {
				if unhealthySince.IsZero() {
					unhealthySince = time.Now()
					c.logger.Warn("DISCORD_CLIENT", "Discord session unhealthy")
				}

				unhealthyDuration := time.Since(unhealthySince)
				c.logger.Warn("DISCORD_CLIENT", fmt.Sprintf("Discord session unhealthy for %v", unhealthyDuration))
			}
		}
	}
}
