package bridge

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/stieneee/mumble-discord-bridge/pkg/logger"
)

// DiscordVoiceConnectionManager manages Discord voice connections with automatic reconnection
// The primary Discord bot connection is managed elsewhere
type DiscordVoiceConnectionManager struct {
	*BaseConnectionManager
	session    *discordgo.Session
	connection *discordgo.VoiceConnection
	guildID    string
	channelID  string
	connMutex  sync.RWMutex

	// Stored opus channel references to avoid data races
	opusSend chan<- []byte
	opusRecv <-chan *discordgo.Packet

	// Simple configuration
	baseReconnectDelay time.Duration // Fixed delay between reconnect attempts

	// Connection generation tracking to detect stale references
	connectionGeneration int32
}

// NewDiscordVoiceConnectionManager creates a new Discord connection manager
func NewDiscordVoiceConnectionManager(session *discordgo.Session, guildID, channelID string, logger logger.Logger, eventEmitter BridgeEventEmitter) *DiscordVoiceConnectionManager {
	base := NewBaseConnectionManager(logger, "discord", eventEmitter)

	return &DiscordVoiceConnectionManager{
		BaseConnectionManager: base,
		session:               session,
		guildID:               guildID,
		channelID:             channelID,
		baseReconnectDelay:    2 * time.Second,
	}
}

// Start runs the main connection loop that handles connection establishment and monitoring
func (d *DiscordVoiceConnectionManager) Start(ctx context.Context) error {
	d.logger.Info("DISCORD_CONN", "Starting Discord connection manager with main loop architecture")

	// Initialize context for proper cancellation chain
	d.InitContext(ctx)

	// Start main connection loop in a goroutine
	go d.mainConnectionLoop(d.ctx)

	return nil
}

// mainConnectionLoop is the main loop that handles connection establishment and monitoring
func (d *DiscordVoiceConnectionManager) mainConnectionLoop(ctx context.Context) {
	defer d.logger.Info("DISCORD_CONN", "Main connection loop exiting")

	for {
		select {
		case <-ctx.Done():
			d.logger.Info("DISCORD_CONN", "Context canceled, exiting connection loop")
			d.disconnectInternal()

			return
		default:
			// Main connection establishment loop
			d.logger.Debug("DISCORD_CONN", "Attempting to establish voice connection")

			if err := d.connectOnce(); err != nil {
				d.logger.Error("DISCORD_CONN", fmt.Sprintf("Connection attempt failed: %v", err))
				d.SetStatus(ConnectionFailed, err)

				// Check if the error is due to primary session being down
				if strings.Contains(err.Error(), "session not ready") {
					d.logger.Info("DISCORD_CONN", "Connection failed due to session not ready, waiting for primary session")
					// Wait longer for primary session to come back
					if d.waitForPrimarySessionReconnection(ctx) {
						d.logger.Info("DISCORD_CONN", "Primary session reconnected, retrying connection immediately")
						continue
					} else {
						d.logger.Error("DISCORD_CONN", "Primary session did not reconnect, will retry with normal delay")
					}
				}

				// Wait before retrying
				select {
				case <-ctx.Done():
					return
				case <-time.After(d.baseReconnectDelay):
					continue
				}
			}

			// Connection successful, start monitoring loop
			d.logger.Info("DISCORD_CONN", "Voice connection established, entering monitoring loop")

			d.monitorConnectionUntilFailure(ctx)

			d.logger.Warn("DISCORD_CONN", "Connection monitoring detected failure, restarting connection process")
			d.disconnectInternal()
		}
	}
}

// monitorConnectionUntilFailure monitors the connection health and exits when failure detected or context canceled
func (d *DiscordVoiceConnectionManager) monitorConnectionUntilFailure(ctx context.Context) {
	d.logger.Debug("DISCORD_CONN", "Starting connection health monitoring - will reconnect immediately on not ready")

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			d.logger.Debug("DISCORD_CONN", "Connection monitoring canceled by context")

			return
		case <-ticker.C:
			// Check primary session health first
			primaryConnected, sessionReason := d.isPrimarySessionConnected()
			if !primaryConnected {
				d.logger.Warn("DISCORD_CONN", fmt.Sprintf("Primary Discord session lost: %s - waiting for reconnection", sessionReason))
				d.SetStatus(ConnectionReconnecting, fmt.Errorf("primary session disconnected: %s", sessionReason))
				
				// Wait for primary session to reconnect instead of immediately disconnecting
				if d.waitForPrimarySessionReconnection(d.ctx) {
					d.logger.Info("DISCORD_CONN", "Primary Discord session reconnected, continuing monitoring")
					continue
				} else {
					d.logger.Error("DISCORD_CONN", "Primary session reconnection timeout or context canceled")
					return
				}
			}

			// Check voice connection health using our safe channel-based approach
			isHealthy := d.isConnectionHealthy()

			if isHealthy {
				// Connection is healthy
				d.SetStatus(ConnectionConnected, nil)
			} else {
				// Connection is not healthy - trigger immediate reconnection
				d.logger.Warn("DISCORD_CONN", "Voice connection not healthy - triggering immediate reconnection")
				d.SetStatus(ConnectionReconnecting, nil)

				return
			}
		}
	}
}

// connectOnce establishes a Discord voice connection once at startup
func (d *DiscordVoiceConnectionManager) connectOnce() error {
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

	// Wait for opus channels to be initialized by discordgo's async event handlers
	// The channels are created in onEvent() which runs asynchronously after ChannelVoiceJoin returns
	maxWaitTime := 5 * time.Second
	startTime := time.Now()
	pollInterval := 50 * time.Millisecond

	d.logger.Debug("DISCORD_CONN", "Waiting for opus channels to be initialized")

	for {
		// Check if channels exist with proper locking to avoid races
		connection.RLock()
		opusSendReady := connection.OpusSend != nil
		opusRecvReady := connection.OpusRecv != nil
		connection.RUnlock()

		if opusSendReady && opusRecvReady {
			// Channels are ready, extract them ONCE and never access VoiceConnection again
			d.connMutex.Lock()
			d.connection = connection
			connection.RLock()
			d.opusSend = connection.OpusSend  // Copy channel reference once
			d.opusRecv = connection.OpusRecv  // Copy channel reference once
			connection.RUnlock()
			atomic.AddInt32(&d.connectionGeneration, 1) // Increment generation for stale reference detection
			d.connMutex.Unlock()
			d.logger.Debug("DISCORD_CONN", "Opus channels successfully extracted")
			break
		}

		if time.Since(startTime) > maxWaitTime {
			d.logger.Error("DISCORD_CONN", "Timeout waiting for opus channels to be initialized")
			d.SetStatus(ConnectionFailed, fmt.Errorf("opus channels initialization timeout"))
			return fmt.Errorf("timeout waiting for opus channels to be initialized")
		}

		// Brief sleep before next poll
		time.Sleep(pollInterval)
	}

	d.SetStatus(ConnectionConnected, nil)
	d.logger.Info("DISCORD_CONN", "Discord voice connection established and ready")

	return nil
}

// disconnectInternal disconnects from Discord voice without changing status
func (d *DiscordVoiceConnectionManager) disconnectInternal() {
	d.connMutex.Lock()
	defer d.connMutex.Unlock()

	// Store local reference and clear fields
	conn := d.connection
	d.connection = nil

	if conn != nil {
		d.logger.Debug("DISCORD_CONN", "Disconnecting from Discord voice")

		// Clear stored channel references
		d.opusSend = nil
		d.opusRecv = nil

		// Safely disconnect - handle case where connection might be in bad state
		func() {
			defer func() {
				if r := recover(); r != nil {
					d.logger.Warn("DISCORD_CONN", fmt.Sprintf("Panic during disconnect (expected if connection was broken): %v", r))
				}
			}()

			if err := conn.Disconnect(); err != nil {
				d.logger.Error("DISCORD_CONN", fmt.Sprintf("Error disconnecting from Discord voice: %v", err))
			}
		}()
	}
}

// waitForSessionReady waits for the Discord session to be ready
func (d *DiscordVoiceConnectionManager) waitForSessionReady(timeout time.Duration) error {
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

// isConnectionHealthy checks if the connection is healthy without accessing VoiceConnection fields
// This eliminates race conditions by only checking our stored channel references
func (d *DiscordVoiceConnectionManager) isConnectionHealthy() bool {
	d.connMutex.RLock()
	defer d.connMutex.RUnlock()

	// Connection is healthy if we have both opus channels
	if d.opusSend == nil || d.opusRecv == nil {
		return false
	}

	// Test if channels are still open by checking if they're closed
	// We use a non-blocking select to avoid interfering with normal operation
	select {
	case _, ok := <-d.opusRecv:
		if !ok {
			// Channel was closed
			return false
		}
		// Put received data back (this shouldn't happen in a health check)
		// but if it does, we just discard it since we're only checking health
	default:
		// Channel not immediately readable, which is normal and healthy
	}

	return true
}

// isPrimarySessionConnected checks if the primary Discord session is connected and ready
func (d *DiscordVoiceConnectionManager) isPrimarySessionConnected() (connected bool, reason string) {
	if d.session == nil {
		return false, "session is nil"
	}

	// Check if session data is ready - this is the primary indicator we use elsewhere
	d.session.RLock()
	dataReady := d.session.DataReady
	d.session.RUnlock()

	if !dataReady {
		return false, "session data not ready"
	}

	// Additional check: see if we have a valid user ID (indicates successful authentication)
	if d.session.State == nil || d.session.State.User == nil || d.session.State.User.ID == "" {
		return false, "session user not available"
	}

	return true, "session connected and ready"
}

// waitForPrimarySessionReconnection waits for the primary Discord session to reconnect
func (d *DiscordVoiceConnectionManager) waitForPrimarySessionReconnection(ctx context.Context) bool {
	d.logger.Info("DISCORD_CONN", "Waiting for primary Discord session to reconnect")
	
	maxWait := 120 * time.Second // Wait up to 2 minutes for reconnection
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	
	start := time.Now()
	
	for {
		select {
		case <-ctx.Done():
			d.logger.Debug("DISCORD_CONN", "Context canceled while waiting for primary session reconnection")
			return false
		case <-ticker.C:
			// Check if primary session has reconnected
			connected, reason := d.isPrimarySessionConnected()
			if connected {
				d.logger.Info("DISCORD_CONN", "Primary Discord session has reconnected successfully")
				return true
			}
			
			d.logger.Debug("DISCORD_CONN", fmt.Sprintf("Primary session still down: %s", reason))
			
			// Check timeout
			if time.Since(start) > maxWait {
				d.logger.Warn("DISCORD_CONN", fmt.Sprintf("Timeout waiting for primary session reconnection (waited %v)", time.Since(start)))
				return false
			}
		}
	}
}

// Stop gracefully stops the Discord connection manager
func (d *DiscordVoiceConnectionManager) Stop() error {
	d.logger.Info("DISCORD_CONN", "Stopping Discord connection manager")

	// Stop the base connection manager (cancels context)
	if err := d.BaseConnectionManager.Stop(); err != nil {
		d.logger.Error("DISCORD_CONN", fmt.Sprintf("Error stopping base connection manager: %v", err))
	}

	// Disconnect from Discord
	d.disconnectInternal()

	d.logger.Info("DISCORD_CONN", "Discord connection manager stopped")

	return nil
}

// GetReadyConnection returns the connection only if channels are available, nil otherwise
// Note: We no longer check VoiceConnection.Ready to avoid race conditions
func (d *DiscordVoiceConnectionManager) GetReadyConnection() *discordgo.VoiceConnection {
	d.connMutex.RLock()
	defer d.connMutex.RUnlock()

	// Only return connection if we have valid opus channels
	if d.connection != nil && d.opusSend != nil && d.opusRecv != nil {
		return d.connection
	}

	return nil
}

// GetOpusChannels safely returns the stored opus send/receive channels
// Never re-reads from VoiceConnection to eliminate race conditions
func (d *DiscordVoiceConnectionManager) GetOpusChannels() (send chan<- []byte, recv <-chan *discordgo.Packet, ready bool) {
	d.connMutex.RLock()
	defer d.connMutex.RUnlock()

	// Only return our stored channel copies, never access VoiceConnection fields
	if d.opusSend != nil && d.opusRecv != nil {
		return d.opusSend, d.opusRecv, true
	}

	return nil, nil, false
}
