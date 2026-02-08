package bridge

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/stieneee/mumble-discord-bridge/pkg/logger"
)

// DiscordVoiceConnectionManager manages Discord voice connections.
// Initial connection is handled by MDB; reconnection is delegated to discordgo's
// built-in reconnect logic (ShouldReconnectOnError + ShouldReconnectVoiceOnSessionError).
// The manager monitors health and reports status. A safety timeout forces a full
// reconnect if voice stays unhealthy for too long.
type DiscordVoiceConnectionManager struct {
	*BaseConnectionManager
	session    *discordgo.Session
	connection *discordgo.VoiceConnection
	guildID    string
	channelID  string
	connMutex  sync.RWMutex

	// Stored opus channel references — set once after initial connection.
	// discordgo reuses the same OpusSend/OpusRecv channels across reconnections
	// (only created if nil in onEvent case 2), so these references stay valid.
	opusSend chan<- []byte
	opusRecv <-chan *discordgo.Packet

	// Simple configuration
	baseReconnectDelay time.Duration // Fixed delay between initial connection retries

	// Event handler cleanup
	eventHandlerRemovers []func()
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
	d.logger.Info("DISCORD_CONN", "Starting Discord connection manager (discordgo handles voice reconnection)")

	// Initialize context for proper cancellation chain
	d.InitContext(ctx)

	// Register voice event handlers for debug logging
	d.registerVoiceEventHandlers()

	// Start main connection loop in a goroutine
	go d.mainConnectionLoop(d.ctx)

	return nil
}

// registerVoiceEventHandlers registers handlers to log VoiceServerUpdate and VoiceStateUpdate events
func (d *DiscordVoiceConnectionManager) registerVoiceEventHandlers() {
	voiceStateHandler := d.session.AddHandler(func(s *discordgo.Session, v *discordgo.VoiceStateUpdate) {
		if v.GuildID != d.guildID {
			return
		}
		if s.State == nil || s.State.User == nil || v.UserID != s.State.User.ID {
			return
		}
		d.logger.Debug("DISCORD_CONN", fmt.Sprintf("VoiceStateUpdate received: ChannelID=%s, SessionID=%s", v.ChannelID, v.SessionID))
	})

	voiceServerHandler := d.session.AddHandler(func(_ *discordgo.Session, v *discordgo.VoiceServerUpdate) {
		if v.GuildID != d.guildID {
			return
		}
		d.logger.Debug("DISCORD_CONN", fmt.Sprintf("VoiceServerUpdate received: Endpoint=%s", v.Endpoint))
	})

	d.eventHandlerRemovers = append(d.eventHandlerRemovers, voiceStateHandler, voiceServerHandler)
}

// removeVoiceEventHandlers removes the registered voice event handlers
func (d *DiscordVoiceConnectionManager) removeVoiceEventHandlers() {
	for _, remover := range d.eventHandlerRemovers {
		remover()
	}
	d.eventHandlerRemovers = nil
}

// UpdateChannel updates the target channel ID for the next connection attempt
func (d *DiscordVoiceConnectionManager) UpdateChannel(channelID string) {
	d.connMutex.Lock()
	defer d.connMutex.Unlock()

	d.channelID = channelID
	d.logger.Info("DISCORD_CONN", fmt.Sprintf("Updated target channel to: %s", channelID))
}

// mainConnectionLoop handles initial connection and delegates reconnection to discordgo.
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

			// Connection successful — monitor status while discordgo handles reconnection
			d.logger.Info("DISCORD_CONN", "Voice connection established, entering monitoring loop")
			d.monitorConnection(ctx)

			// Monitor exited due to safety timeout or context cancellation
			d.logger.Warn("DISCORD_CONN", "Connection monitoring exited, forcing full reconnect")
			d.disconnectInternal()
		}
	}
}

// monitorConnection monitors voice connection health and reports status changes.
// discordgo handles reconnection internally; this loop just tracks Ready state.
// Exits only on context cancellation or safety timeout (>2 min unhealthy).
func (d *DiscordVoiceConnectionManager) monitorConnection(ctx context.Context) {
	d.logger.Debug("DISCORD_CONN", "Starting connection health monitoring (discordgo handles reconnection)")

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	const safetyTimeout = 2 * time.Minute
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
					d.logger.Info("DISCORD_CONN", "Voice connection recovered (discordgo reconnected)")
					unhealthySince = time.Time{}
				}
				wasHealthy = true
				d.SetStatus(ConnectionConnected, nil)
			} else {
				if wasHealthy {
					d.logger.Warn("DISCORD_CONN", "Voice connection unhealthy, waiting for discordgo to reconnect")
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

// connectOnce establishes a Discord voice connection with timeout control
func (d *DiscordVoiceConnectionManager) connectOnce() error {
	d.SetStatus(ConnectionConnecting, nil)
	d.logger.Debug("DISCORD_CONN", fmt.Sprintf("Connecting to Discord voice: Guild=%s, Channel=%s", d.guildID, d.channelID))

	// Validate session state
	if d.session == nil || d.session.State == nil || d.session.State.User == nil {
		return fmt.Errorf("discord session not ready")
	}

	// Wait for session to be fully ready
	d.logger.Debug("DISCORD_CONN", "Waiting for Discord session to be ready")
	if err := d.waitForSessionReady(10 * time.Second); err != nil {
		d.logger.Error("DISCORD_CONN", fmt.Sprintf("Session not ready within timeout: %v", err))
		d.SetStatus(ConnectionFailed, err)

		return fmt.Errorf("session not ready: %w", err)
	}

	// Run ChannelVoiceJoin in a goroutine with our own timeout
	type connResult struct {
		conn *discordgo.VoiceConnection
		err  error
	}
	resultChan := make(chan connResult, 1)

	go func() {
		d.logger.Debug("DISCORD_CONN", fmt.Sprintf("Attempting voice connection to Guild=%s, Channel=%s", d.guildID, d.channelID))
		conn, err := d.session.ChannelVoiceJoin(d.guildID, d.channelID, false, false)
		resultChan <- connResult{conn: conn, err: err}
	}()

	connectionTimeout := 30 * time.Second
	select {
	case <-d.ctx.Done():
		d.logger.Info("DISCORD_CONN", "Connection attempt canceled by context")

		return fmt.Errorf("connection canceled")

	case <-time.After(connectionTimeout):
		d.logger.Error("DISCORD_CONN", fmt.Sprintf("Voice connection timeout after %v", connectionTimeout))
		d.SetStatus(ConnectionFailed, fmt.Errorf("connection timeout"))

		return fmt.Errorf("voice connection timeout")

	case result := <-resultChan:
		if result.err != nil {
			d.logger.Error("DISCORD_CONN", fmt.Sprintf("Voice connection failed: %v", result.err))
			d.SetStatus(ConnectionFailed, result.err)

			return fmt.Errorf("failed to join voice channel: %w", result.err)
		}

		connection := result.conn
		d.logger.Debug("DISCORD_CONN", "ChannelVoiceJoin returned successfully, extracting opus channels")

		// Wait for opus channels to be initialized
		if err := d.waitForOpusChannels(connection, 5*time.Second); err != nil {
			d.logger.Error("DISCORD_CONN", fmt.Sprintf("Failed to get opus channels: %v", err))
			d.SetStatus(ConnectionFailed, err)

			return err
		}

		d.SetStatus(ConnectionConnected, nil)
		d.logger.Info("DISCORD_CONN", "Discord voice connection established and ready")

		return nil
	}
}

// waitForOpusChannels waits for opus channels to be initialized and stores them.
func (d *DiscordVoiceConnectionManager) waitForOpusChannels(connection *discordgo.VoiceConnection, timeout time.Duration) error {
	startTime := time.Now()
	pollInterval := 50 * time.Millisecond

	d.logger.Debug("DISCORD_CONN", "Waiting for voice connection to become ready")

	var opusSend chan []byte
	var opusRecv chan *discordgo.Packet

	for {
		connection.RLock()
		ready := connection.Ready
		if ready {
			opusSend = connection.OpusSend
			opusRecv = connection.OpusRecv
		}
		connection.RUnlock()

		if ready && opusSend != nil && opusRecv != nil {
			d.logger.Debug("DISCORD_CONN", "Voice connection ready with opus channels")

			break
		}

		if time.Since(startTime) > timeout {
			if !ready {
				d.logger.Error("DISCORD_CONN", "Timeout waiting for voice connection to become ready")

				return fmt.Errorf("voice connection readiness timeout")
			}

			d.logger.Error("DISCORD_CONN", "Timeout waiting for opus channels after Ready=true")

			return fmt.Errorf("opus channels not initialized within timeout")
		}

		select {
		case <-d.ctx.Done():
			return fmt.Errorf("context canceled while waiting for voice connection")
		default:
		}

		time.Sleep(pollInterval)
	}

	// Store channels once. discordgo reuses the same channels across reconnections.
	d.connMutex.Lock()
	d.connection = connection
	d.opusSend = opusSend
	d.opusRecv = opusRecv
	d.connMutex.Unlock()
	d.logger.Debug("DISCORD_CONN", "Opus channels successfully extracted")

	return nil
}

// disconnectInternal disconnects from Discord voice
func (d *DiscordVoiceConnectionManager) disconnectInternal() {
	d.connMutex.Lock()
	defer d.connMutex.Unlock()

	conn := d.connection
	d.connection = nil

	if conn != nil {
		d.logger.Debug("DISCORD_CONN", "Disconnecting from Discord voice")

		d.opusSend = nil
		d.opusRecv = nil

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
		// Use TryRLock to avoid blocking if the session mutex is deadlocked.
		var dataReady bool
		if d.session.TryRLock() {
			dataReady = d.session.DataReady
			d.session.RUnlock()
		}

		if dataReady {
			d.logger.Debug("DISCORD_CONN", "Session DataReady is true")

			return nil
		}

		if time.Since(start) > timeout {
			return fmt.Errorf("timeout waiting for session to be ready")
		}

		select {
		case <-ticker.C:
		case <-d.ctx.Done():
			return fmt.Errorf("context canceled while waiting for session")
		}
	}
}

// isConnectionHealthy checks if the voice connection is active.
func (d *DiscordVoiceConnectionManager) isConnectionHealthy() bool {
	d.connMutex.RLock()
	defer d.connMutex.RUnlock()

	if d.opusSend == nil || d.opusRecv == nil || d.connection == nil {
		return false
	}

	d.connection.RLock()
	ready := d.connection.Ready
	d.connection.RUnlock()

	return ready
}

// Stop gracefully stops the Discord connection manager
func (d *DiscordVoiceConnectionManager) Stop() error {
	d.logger.Info("DISCORD_CONN", "Stopping Discord connection manager")

	d.removeVoiceEventHandlers()

	if err := d.BaseConnectionManager.Stop(); err != nil {
		d.logger.Error("DISCORD_CONN", fmt.Sprintf("Error stopping base connection manager: %v", err))
	}

	d.disconnectInternal()

	d.logger.Info("DISCORD_CONN", "Discord connection manager stopped")

	return nil
}

// GetReadyConnection returns the connection only if voice is ready, nil otherwise
func (d *DiscordVoiceConnectionManager) GetReadyConnection() *discordgo.VoiceConnection {
	d.connMutex.RLock()
	defer d.connMutex.RUnlock()

	if d.connection == nil || d.opusSend == nil || d.opusRecv == nil {
		return nil
	}

	d.connection.RLock()
	ready := d.connection.Ready
	d.connection.RUnlock()

	if !ready {
		return nil
	}

	return d.connection
}

// GetOpusChannels safely returns the stored opus send/receive channels.
// Returns not-ready when the voice connection is down (Ready=false), so callers
// sink packets instead of writing to channels whose goroutines are stopped.
func (d *DiscordVoiceConnectionManager) GetOpusChannels() (send chan<- []byte, recv <-chan *discordgo.Packet, ready bool) {
	d.connMutex.RLock()
	defer d.connMutex.RUnlock()

	if d.opusSend == nil || d.opusRecv == nil || d.connection == nil {
		return nil, nil, false
	}

	// Check VoiceConnection.Ready — during discordgo reconnection, Ready is false
	// and opus goroutines are stopped, so sending/receiving would fail
	d.connection.RLock()
	isReady := d.connection.Ready
	d.connection.RUnlock()

	if !isReady {
		return nil, nil, false
	}

	return d.opusSend, d.opusRecv, true
}
