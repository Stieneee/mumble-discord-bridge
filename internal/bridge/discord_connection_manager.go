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

// Start establishes the Discord connection once and lets the library handle reconnection
func (d *DiscordConnectionManager) Start(ctx context.Context) error {
	d.logger.Info("DISCORD_CONN", "Starting Discord connection manager")

	// Initialize context for proper cancellation chain
	d.InitContext(ctx)

	// Establish connection once - let discordgo library handle reconnection
	if err := d.connectOnce(); err != nil {
		d.logger.Error("DISCORD_CONN", fmt.Sprintf("Initial connection failed: %v", err))

		return err
	}

	// Start simple connection monitoring (not management)
	go d.monitorConnection(d.ctx)

	return nil
}

// monitorConnection monitors the Discord connection state without managing reconnection
// The discordgo library handles reconnection automatically
func (d *DiscordConnectionManager) monitorConnection(ctx context.Context) {
	d.logger.Info("DISCORD_CONN", "Starting connection monitoring (library handles reconnection)")

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			d.logger.Info("DISCORD_CONN", "Connection monitoring canceled")
			d.disconnectInternal()

			return
		case <-ticker.C:
			// Just update our status based on actual connection state
			d.updateConnectionStatus()
		}
	}
}

// connectOnce establishes a Discord voice connection once at startup
func (d *DiscordConnectionManager) connectOnce() error {
	d.SetStatus(ConnectionConnecting, nil)
	d.logger.Debug("DISCORD_CONN", fmt.Sprintf("Connecting to Discord voice: Guild=%s, Channel=%s", d.guildID, d.channelID))

	// Validate session state
	if d.session == nil || d.session.State == nil || d.session.State.User == nil {
		return fmt.Errorf("discord session not ready")
	}

	// Wait for session to be fully ready to avoid race condition
	d.logger.Debug("DISCORD_CONN", "Waiting for Discord session to be ready")
	if err := d.waitForSessionReady(10 * time.Second); err != nil {
		d.logger.Error("DISCORD_CONN", fmt.Sprintf("Session not ready within timeout: %v", err))
		d.SetStatus(ConnectionFailed, err)

		return fmt.Errorf("session not ready: %w", err)
	}

	// Attempt voice connection - library will reuse existing connection if present
	d.logger.Debug("DISCORD_CONN", fmt.Sprintf("Attempting voice connection to Guild=%s, Channel=%s", d.guildID, d.channelID))
	connection, err := d.session.ChannelVoiceJoin(d.guildID, d.channelID, false, false)
	if err != nil {
		d.logger.Error("DISCORD_CONN", fmt.Sprintf("Voice connection failed: %v", err))
		d.SetStatus(ConnectionFailed, err)

		return fmt.Errorf("failed to join voice channel: %w", err)
	}

	// Store connection
	d.connMutex.Lock()
	d.connection = connection
	d.connMutex.Unlock()

	d.SetStatus(ConnectionConnected, nil)
	d.logger.Info("DISCORD_CONN", "Discord voice connection established successfully")

	return nil
}

// disconnectInternal disconnects from Discord voice without changing status
func (d *DiscordConnectionManager) disconnectInternal() {
	d.connMutex.Lock()
	defer d.connMutex.Unlock()

	if d.connection != nil {
		d.logger.Debug("DISCORD_CONN", "Disconnecting from Discord voice")

		// Store local reference and clear field
		conn := d.connection
		d.connection = nil

		// Disconnect synchronously since this is only called during shutdown
		if err := conn.Disconnect(); err != nil {
			d.logger.Error("DISCORD_CONN", fmt.Sprintf("Error disconnecting from Discord voice: %v", err))
		}
	}
}

// waitForSessionReady waits for the Discord session to be ready
func (d *DiscordConnectionManager) waitForSessionReady(timeout time.Duration) error {
	start := time.Now()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		// Check if session is ready with proper synchronization
		// We only check DataReady since that's what the race condition involves
		d.session.RLock()
		dataReady := d.session.DataReady
		d.session.RUnlock()

		if dataReady {
			d.logger.Debug("DISCORD_CONN", "Session DataReady is true")

			return nil
		}

		// Check timeout
		if time.Since(start) > timeout {
			return fmt.Errorf("timeout waiting for session to be ready")
		}

		// Wait before next check
		select {
		case <-ticker.C:
			// Continue loop
		case <-d.ctx.Done():
			return fmt.Errorf("context canceled while waiting for session")
		}
	}
}

// checkConnectionReady safely checks if the connection is ready with proper locking
func (d *DiscordConnectionManager) checkConnectionReady() (connection *discordgo.VoiceConnection, isReady bool) {
	d.connMutex.RLock()
	connection = d.connection
	d.connMutex.RUnlock()

	if connection == nil {
		return nil, false
	}

	// Check Ready status safely
	defer func() {
		if r := recover(); r != nil {
			d.logger.Error("DISCORD_CONN", fmt.Sprintf("Panic checking Ready state: %v", r))
			isReady = false
		}
	}()

	connection.RLock()
	isReady = connection.Ready
	connection.RUnlock()

	return connection, isReady
}

// updateConnectionStatus updates the connection manager status based on actual Discord connection state
func (d *DiscordConnectionManager) updateConnectionStatus() {
	conn, isReady := d.checkConnectionReady()

	if conn == nil {
		d.SetStatus(ConnectionDisconnected, fmt.Errorf("no connection"))

		return
	}

	if isReady {
		d.SetStatus(ConnectionConnected, nil)
	} else {
		// Connection exists but not ready - library is probably reconnecting
		d.SetStatus(ConnectionReconnecting, nil)
	}
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
// Deprecated: Use GetReadyConnection() instead for safer access
func (d *DiscordConnectionManager) GetConnection() *discordgo.VoiceConnection {
	d.connMutex.RLock()
	defer d.connMutex.RUnlock()

	return d.connection
}

// GetReadyConnection returns the connection only if it's ready, nil otherwise
func (d *DiscordConnectionManager) GetReadyConnection() *discordgo.VoiceConnection {
	connection, isReady := d.checkConnectionReady()
	if isReady {
		return connection
	}

	return nil
}

// IsConnectionReady safely checks if the current connection is ready for use
func (d *DiscordConnectionManager) IsConnectionReady() bool {
	_, isReady := d.checkConnectionReady()

	return isReady
}

// GetOpusChannels safely returns the opus send/receive channels
func (d *DiscordConnectionManager) GetOpusChannels() (send chan<- []byte, recv <-chan *discordgo.Packet, ready bool) {
	connection, isReady := d.checkConnectionReady()

	if connection == nil || !isReady {
		return nil, nil, false
	}

	// Get channels safely
	defer func() {
		if r := recover(); r != nil {
			d.logger.Error("DISCORD_CONN", fmt.Sprintf("Panic getting opus channels: %v", r))
			send = nil
			recv = nil
			ready = false
		}
	}()

	connection.RLock()
	send = connection.OpusSend
	recv = connection.OpusRecv
	connection.RUnlock()

	return send, recv, true
}

// GetConnectionInfo returns Discord-specific connection information
func (d *DiscordConnectionManager) GetConnectionInfo() map[string]any {
	d.connMutex.RLock()
	defer d.connMutex.RUnlock()

	info := map[string]any{
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
