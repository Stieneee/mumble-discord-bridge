package bridge

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/stieneee/mumble-discord-bridge/pkg/logger"
)

// DiscordConnectionManager manages Discord voice connections with automatic reconnection
type DiscordConnectionManager struct {
	*BaseConnectionManager
	session    *discordgo.Session
	connection *discordgo.VoiceConnection
	guildID    string
	channelID  string
	connMutex  sync.RWMutex
}

// NewDiscordConnectionManager creates a new Discord connection manager
func NewDiscordConnectionManager(session *discordgo.Session, guildID, channelID string, logger logger.Logger, eventEmitter BridgeEventEmitter) *DiscordConnectionManager {
	base := NewBaseConnectionManager(logger, "discord", eventEmitter)

	return &DiscordConnectionManager{
		BaseConnectionManager: base,
		session:               session,
		guildID:               guildID,
		channelID:             channelID,
	}
}

// Start begins the Discord connection process with automatic reconnection
func (d *DiscordConnectionManager) Start(ctx context.Context) error {
	d.logger.Info("DISCORD_CONN", "Starting Discord connection manager")

	// Initialize context for proper cancellation chain
	d.InitContext(ctx)

	// Start connection management goroutine
	go d.connectionLoop(d.ctx)

	return nil
}

// connectionLoop manages the connection lifecycle with reconnection logic
func (d *DiscordConnectionManager) connectionLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			d.logger.Info("DISCORD_CONN", "Connection loop cancelled")
			d.disconnectInternal()
			return
		default:
		}

		// Attempt connection
		if err := d.connect(); err != nil {
			d.logger.Error("DISCORD_CONN", fmt.Sprintf("Connection failed: %v", err))
			d.SetStatus(ConnectionReconnecting, err)

			// Wait before retrying
			select {
			case <-time.After(5 * time.Second):
				continue
			case <-ctx.Done():
				return
			}
		}

		// Connection successful
		d.SetStatus(ConnectionConnected, nil)
		d.logger.Info("DISCORD_CONN", "Discord voice connection established")

		// Wait for the connection to be lost (Discord library handles this)
		// We'll rely on the voice connection's error channel or Ready state
		select {
		case <-ctx.Done():
			return
		case <-d.waitForConnectionLoss(ctx):
			// Connection lost, prepare for reconnection
			d.SetStatus(ConnectionReconnecting, nil)
			d.logger.Warn("DISCORD_CONN", "Discord connection lost, attempting reconnection")
		}
	}
}

// connect establishes a Discord voice connection
func (d *DiscordConnectionManager) connect() error {
	d.SetStatus(ConnectionConnecting, nil)
	d.logger.Debug("DISCORD_CONN", fmt.Sprintf("Connecting to Discord voice: Guild=%s, Channel=%s", d.guildID, d.channelID))

	// Validate session state
	if d.session == nil || d.session.State == nil || d.session.State.User == nil {
		return fmt.Errorf("discord session not ready")
	}

	// Disconnect any existing connection
	d.disconnectInternal()

	// Attempt voice connection
	d.logger.Debug("DISCORD_CONN", fmt.Sprintf("Attempting voice connection to Guild=%s, Channel=%s", d.guildID, d.channelID))
	connection, err := d.session.ChannelVoiceJoin(d.guildID, d.channelID, false, false)
	if err != nil {
		d.logger.Error("DISCORD_CONN", fmt.Sprintf("Voice connection failed: %v", err))
		return fmt.Errorf("failed to join voice channel: %w", err)
	}

	// Store connection
	d.connMutex.Lock()
	d.connection = connection
	d.connMutex.Unlock()

	d.logger.Debug("DISCORD_CONN", "Discord voice connection established successfully")
	return nil
}

// disconnectInternal disconnects from Discord voice without changing status
func (d *DiscordConnectionManager) disconnectInternal() {
	d.connMutex.Lock()
	defer d.connMutex.Unlock()

	if d.connection != nil {
		d.logger.Debug("DISCORD_CONN", "Disconnecting from Discord voice")
		if err := d.connection.Disconnect(); err != nil {
			d.logger.Error("DISCORD_CONN", fmt.Sprintf("Error disconnecting from Discord voice: %v", err))
		}
		d.connection = nil
	}
}

// waitForConnectionLoss waits for the Discord connection to be lost by monitoring the Ready state
func (d *DiscordConnectionManager) waitForConnectionLoss(ctx context.Context) <-chan bool {
	lostChan := make(chan bool, 1)

	go func() {
		defer func() {
			if r := recover(); r != nil {
				d.logger.Error("DISCORD_CONN", fmt.Sprintf("Connection loss monitoring panic recovered: %v", r))
			}
			close(lostChan)
		}()

		// Check connection state periodically
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				// Add additional context check to prevent races
				if ctx.Err() != nil {
					return
				}

				d.connMutex.RLock()
				conn := d.connection
				d.connMutex.RUnlock()

				// If connection is nil or not ready, consider it lost
				if conn == nil || !conn.Ready {
					select {
					case lostChan <- true:
					case <-ctx.Done():
						return
					default:
						// Channel is full or closed, connection already considered lost
					}
					return
				}
			}
		}
	}()

	return lostChan
}

// Stop gracefully stops the Discord connection manager
func (d *DiscordConnectionManager) Stop() error {
	d.logger.Info("DISCORD_CONN", "Stopping Discord connection manager")

	// Stop the base connection manager (cancels context)
	if err := d.BaseConnectionManager.Stop(); err != nil {
		d.logger.Error("DISCORD_CONN", fmt.Sprintf("Error stopping base connection manager: %v", err))
	}

	// Disconnect from Discord
	d.disconnectInternal()

	return nil
}

// GetConnection returns the current Discord voice connection (thread-safe)
func (d *DiscordConnectionManager) GetConnection() *discordgo.VoiceConnection {
	d.connMutex.RLock()
	defer d.connMutex.RUnlock()
	return d.connection
}

// GetConnectionInfo returns Discord-specific connection information
func (d *DiscordConnectionManager) GetConnectionInfo() map[string]interface{} {
	d.connMutex.RLock()
	defer d.connMutex.RUnlock()

	info := map[string]interface{}{
		"type":      "discord",
		"guildID":   d.guildID,
		"channelID": d.channelID,
		"ready":     false,
	}

	if d.connection != nil {
		info["ready"] = d.connection.Ready
		info["guildID"] = d.connection.GuildID
	}

	return info
}

// GetGuildID returns the Discord guild ID
func (d *DiscordConnectionManager) GetGuildID() string {
	return d.guildID
}

// GetChannelID returns the Discord channel ID
func (d *DiscordConnectionManager) GetChannelID() string {
	return d.channelID
}
