package bridge

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/stieneee/mumble-discord-bridge/internal/discord"
	"github.com/stieneee/mumble-discord-bridge/pkg/logger"
)

// DiscordVoiceConnectionManager manages Discord voice connections.
// Voice reconnection is handled by disgo internally. The manager monitors
// health and reports status; a safety timeout forces a full reconnect if voice
// stays unhealthy for too long.
type DiscordVoiceConnectionManager struct {
	*BaseConnectionManager
	discordClient discord.Client
	voiceConn     discord.VoiceConnection
	guildID       string
	channelID     string
	connMutex     sync.RWMutex

	// Simple configuration
	baseReconnectDelay time.Duration
}

// NewDiscordVoiceConnectionManager creates a new Discord connection manager
func NewDiscordVoiceConnectionManager(client discord.Client, guildID, channelID string, logger logger.Logger, eventEmitter BridgeEventEmitter) *DiscordVoiceConnectionManager {
	base := NewBaseConnectionManager(logger, "discord", eventEmitter)

	return &DiscordVoiceConnectionManager{
		BaseConnectionManager: base,
		discordClient:         client,
		guildID:               guildID,
		channelID:             channelID,
		baseReconnectDelay:    2 * time.Second,
	}
}

// Start runs the main connection loop
func (d *DiscordVoiceConnectionManager) Start(ctx context.Context) error {
	d.logger.Info("DISCORD_CONN", "Starting Discord connection manager (disgo+godave with DAVE E2EE)")

	d.InitContext(ctx)
	go d.mainConnectionLoop(d.ctx)

	return nil
}

// UpdateChannel updates the target channel ID
func (d *DiscordVoiceConnectionManager) UpdateChannel(channelID string) {
	d.connMutex.Lock()
	defer d.connMutex.Unlock()

	d.channelID = channelID
	d.logger.Info("DISCORD_CONN", fmt.Sprintf("Updated target channel to: %s", channelID))
}

// mainConnectionLoop handles initial connection and monitoring.
// Only loops back to connectOnce if the safety timeout fires (voice unhealthy >2min).
func (d *DiscordVoiceConnectionManager) mainConnectionLoop(ctx context.Context) {
	defer d.logger.Info("DISCORD_CONN", "Main connection loop exiting")

	for {
		select {
		case <-ctx.Done():
			d.logger.Info("DISCORD_CONN", "Context canceled, exiting connection loop")
			d.disconnectInternal()

			return
		default:
			d.logger.Debug("DISCORD_CONN", "Attempting to establish voice connection")

			if err := d.connectOnce(); err != nil {
				d.logger.Error("DISCORD_CONN", fmt.Sprintf("Connection attempt failed: %v", err))
				d.SetStatus(ConnectionFailed, err)

				// Wait before retrying
				select {
				case <-ctx.Done():
					return
				case <-time.After(d.baseReconnectDelay):
					continue
				}
			}

			// Connection successful — monitor status
			d.logger.Info("DISCORD_CONN", "Voice connection established, entering monitoring loop")
			d.monitorConnection(ctx)

			// Monitor exited due to safety timeout or context cancellation
			d.logger.Warn("DISCORD_CONN", "Connection monitoring exited, forcing full reconnect")
			d.disconnectInternal()
		}
	}
}

// monitorConnection monitors voice connection health and reports status changes.
// Exits only on context cancellation or safety timeout (>2 min unhealthy).
func (d *DiscordVoiceConnectionManager) monitorConnection(ctx context.Context) {
	d.logger.Debug("DISCORD_CONN", "Starting connection health monitoring")

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	const safetyTimeout = 30 * time.Second
	var unhealthySince time.Time
	wasHealthy := true

	for {
		select {
		case <-ctx.Done():
			d.logger.Debug("DISCORD_CONN", "Connection monitoring canceled by context")

			return
		case <-ticker.C:
			healthy := d.isConnectionHealthy()

			if healthy {
				if !wasHealthy {
					d.logger.Info("DISCORD_CONN", "Voice connection recovered")
					unhealthySince = time.Time{}
				}
				wasHealthy = true
				d.SetStatus(ConnectionConnected, nil)
			} else {
				if wasHealthy {
					d.logger.Warn("DISCORD_CONN", "Voice connection unhealthy")
					unhealthySince = time.Now()
					d.SetStatus(ConnectionReconnecting, nil)
				}
				wasHealthy = false

				// Safety valve: if unhealthy for too long, force full disconnect/reconnect
				if !unhealthySince.IsZero() && time.Since(unhealthySince) > safetyTimeout {
					d.logger.Error("DISCORD_CONN", fmt.Sprintf("Voice connection unhealthy for >%v, forcing full reconnect", safetyTimeout))

					return
				}
			}
		}
	}
}

// connectOnce establishes a Discord voice connection
func (d *DiscordVoiceConnectionManager) connectOnce() error {
	d.SetStatus(ConnectionConnecting, nil)
	d.logger.Debug("DISCORD_CONN", fmt.Sprintf("Connecting to Discord voice: Guild=%s, Channel=%s", d.guildID, d.channelID))

	// Wait for client to be ready
	if err := d.waitForClientReady(10 * time.Second); err != nil {
		d.SetStatus(ConnectionFailed, err)

		return fmt.Errorf("client not ready: %w", err)
	}

	// Create voice connection
	voiceConn, err := d.discordClient.CreateVoiceConnection(d.guildID)
	if err != nil {
		d.SetStatus(ConnectionFailed, err)

		return fmt.Errorf("failed to create voice connection: %w", err)
	}

	// Open with timeout — derive from parent context so cancellation also
	// terminates the Open call, preventing goroutine leaks.
	const connectionTimeout = 30 * time.Second
	openCtx, openCancel := context.WithTimeout(d.ctx, connectionTimeout)
	defer openCancel()

	if err := voiceConn.Open(openCtx, d.channelID); err != nil {
		d.logger.Error("DISCORD_CONN", fmt.Sprintf("Voice connection failed: %v", err))
		d.SetStatus(ConnectionFailed, err)

		return fmt.Errorf("failed to join voice channel: %w", err)
	}

	d.connMutex.Lock()
	d.voiceConn = voiceConn
	d.connMutex.Unlock()

	d.SetStatus(ConnectionConnected, nil)
	d.logger.Info("DISCORD_CONN", "Discord voice connection established with DAVE E2EE")

	return nil
}

// waitForClientReady waits for the Discord client to be ready
func (d *DiscordVoiceConnectionManager) waitForClientReady(timeout time.Duration) error {
	start := time.Now()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		if d.discordClient.IsReady() {
			d.logger.Debug("DISCORD_CONN", "Discord client is ready")

			return nil
		}

		if time.Since(start) > timeout {
			return fmt.Errorf("timeout waiting for client to be ready")
		}

		select {
		case <-ticker.C:
		case <-d.ctx.Done():
			return fmt.Errorf("context canceled while waiting for client")
		}
	}
}

// disconnectInternal disconnects from Discord voice
func (d *DiscordVoiceConnectionManager) disconnectInternal() {
	d.connMutex.Lock()
	defer d.connMutex.Unlock()

	conn := d.voiceConn
	d.voiceConn = nil

	if conn != nil {
		d.logger.Debug("DISCORD_CONN", "Disconnecting from Discord voice")

		func() {
			defer func() {
				if r := recover(); r != nil {
					d.logger.Warn("DISCORD_CONN", fmt.Sprintf("Panic during disconnect (expected if connection was broken): %v", r))
				}
			}()

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := conn.Close(ctx); err != nil {
				d.logger.Error("DISCORD_CONN", fmt.Sprintf("Error disconnecting: %v", err))
			}
		}()
	}
}

// isConnectionHealthy checks if the voice connection is active.
// Checks both local state (IsReady) and the actual voice gateway status
// (IsGatewayReady) to detect stale sessions where the local state says
// "ready" but the voice gateway is stuck in a reconnect loop.
func (d *DiscordVoiceConnectionManager) isConnectionHealthy() bool {
	d.connMutex.RLock()
	defer d.connMutex.RUnlock()

	if d.voiceConn == nil {
		return false
	}

	return d.voiceConn.IsReady() && d.voiceConn.IsGatewayReady()
}

// Stop gracefully stops the Discord connection manager
func (d *DiscordVoiceConnectionManager) Stop() error {
	d.logger.Info("DISCORD_CONN", "Stopping Discord connection manager")

	if err := d.BaseConnectionManager.Stop(); err != nil {
		d.logger.Error("DISCORD_CONN", fmt.Sprintf("Error stopping base connection manager: %v", err))
	}

	d.disconnectInternal()

	d.logger.Info("DISCORD_CONN", "Discord connection manager stopped")

	return nil
}

// GetVoiceConnection returns the voice connection if ready, nil otherwise
func (d *DiscordVoiceConnectionManager) GetVoiceConnection() discord.VoiceConnection {
	d.connMutex.RLock()
	defer d.connMutex.RUnlock()

	if d.voiceConn == nil || !d.voiceConn.IsReady() {
		return nil
	}

	return d.voiceConn
}
