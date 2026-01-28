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

	// Event tracking for manual voice connection flow
	voiceEventMutex      sync.Mutex
	pendingVoiceState    *discordgo.VoiceStateUpdate  // Our voice state update
	pendingVoiceServer   *discordgo.VoiceServerUpdate // Voice server info
	voiceEventGeneration int32                        // Track which connection attempt events belong to
	eventHandlerRemovers []func()                     // Functions to remove event handlers
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
	d.logger.Info("DISCORD_CONN", "Starting Discord connection manager with manual voice flow")

	// Initialize context for proper cancellation chain
	d.InitContext(ctx)

	// Register voice event handlers for tracking connection progress
	d.registerVoiceEventHandlers()

	// Start main connection loop in a goroutine
	go d.mainConnectionLoop(d.ctx)

	return nil
}

// registerVoiceEventHandlers registers handlers to track VoiceServerUpdate and VoiceStateUpdate events
func (d *DiscordVoiceConnectionManager) registerVoiceEventHandlers() {
	// Handler for VoiceStateUpdate - tracks our voice state changes
	voiceStateHandler := d.session.AddHandler(func(s *discordgo.Session, v *discordgo.VoiceStateUpdate) {
		// Only track our own voice state updates for our guild
		if v.GuildID != d.guildID {
			return
		}
		if s.State == nil || s.State.User == nil || v.UserID != s.State.User.ID {
			return
		}

		d.voiceEventMutex.Lock()
		defer d.voiceEventMutex.Unlock()

		d.pendingVoiceState = v
		d.logger.Debug("DISCORD_CONN", fmt.Sprintf("VoiceStateUpdate received: ChannelID=%s, SessionID=%s", v.ChannelID, v.SessionID))
	})

	// Handler for VoiceServerUpdate - provides endpoint and token for voice connection
	voiceServerHandler := d.session.AddHandler(func(_ *discordgo.Session, v *discordgo.VoiceServerUpdate) {
		// Only track events for our guild
		if v.GuildID != d.guildID {
			return
		}

		d.voiceEventMutex.Lock()
		defer d.voiceEventMutex.Unlock()

		d.pendingVoiceServer = v
		d.logger.Debug("DISCORD_CONN", fmt.Sprintf("VoiceServerUpdate received: Endpoint=%s", v.Endpoint))
	})

	// Store removers for cleanup
	d.eventHandlerRemovers = append(d.eventHandlerRemovers, voiceStateHandler, voiceServerHandler)
}

// removeVoiceEventHandlers removes the registered voice event handlers
func (d *DiscordVoiceConnectionManager) removeVoiceEventHandlers() {
	for _, remover := range d.eventHandlerRemovers {
		remover()
	}
	d.eventHandlerRemovers = nil
}

// clearPendingVoiceEvents clears the pending voice events and increments generation
func (d *DiscordVoiceConnectionManager) clearPendingVoiceEvents() {
	d.voiceEventMutex.Lock()
	defer d.voiceEventMutex.Unlock()

	d.pendingVoiceState = nil
	d.pendingVoiceServer = nil
	atomic.AddInt32(&d.voiceEventGeneration, 1)
}

// GetVoiceEventInfo returns information about received voice events for debugging
func (d *DiscordVoiceConnectionManager) GetVoiceEventInfo() (hasState, hasServer bool, endpoint string) {
	d.voiceEventMutex.Lock()
	defer d.voiceEventMutex.Unlock()

	hasState = d.pendingVoiceState != nil
	hasServer = d.pendingVoiceServer != nil
	if d.pendingVoiceServer != nil {
		endpoint = d.pendingVoiceServer.Endpoint
	}

	return hasState, hasServer, endpoint
}

// UpdateChannel updates the target channel ID for the next connection attempt
func (d *DiscordVoiceConnectionManager) UpdateChannel(channelID string) {
	d.connMutex.Lock()
	defer d.connMutex.Unlock()

	d.channelID = channelID
	d.logger.Info("DISCORD_CONN", fmt.Sprintf("Updated target channel to: %s", channelID))
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
					}

					d.logger.Error("DISCORD_CONN", "Primary session did not reconnect, will retry with normal delay")
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
					d.logger.Info("DISCORD_CONN", "Primary Discord session reconnected, forcing voice reconnection")
					// After network loss and primary session recovery, the voice WebSocket connection
					// is likely broken. Force a full voice reconnection to ensure the audio path
					// is re-established.
					d.SetStatus(ConnectionReconnecting, fmt.Errorf("forcing voice reconnect after primary session recovery"))

					return // Exit monitoring to trigger mainConnectionLoop reconnect
				}

				d.logger.Error("DISCORD_CONN", "Primary session reconnection timeout or context canceled")

				return
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

// connectOnce establishes a Discord voice connection with non-blocking timeout control
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

	// Clear any pending voice events from previous attempts
	d.clearPendingVoiceEvents()

	// Run ChannelVoiceJoin in a goroutine with our own timeout
	// This gives us control over the connection timeout
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

	// Wait for connection with our own timeout
	connectionTimeout := 30 * time.Second
	select {
	case <-d.ctx.Done():
		d.logger.Info("DISCORD_CONN", "Connection attempt canceled by context")

		return fmt.Errorf("connection canceled")

	case <-time.After(connectionTimeout):
		d.logger.Error("DISCORD_CONN", fmt.Sprintf("Voice connection timeout after %v", connectionTimeout))
		// Log what voice events we received for debugging
		d.voiceEventMutex.Lock()
		hasState := d.pendingVoiceState != nil
		hasServer := d.pendingVoiceServer != nil
		d.voiceEventMutex.Unlock()
		d.logger.Error("DISCORD_CONN", fmt.Sprintf("Voice events received - VoiceState: %v, VoiceServer: %v", hasState, hasServer))
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

// waitForOpusChannels waits for opus channels to be initialized and extracts them.
// We check Ready, OpusSend, and OpusRecv together in a single locked section to avoid
// a race window between Ready becoming true and channels being set.
func (d *DiscordVoiceConnectionManager) waitForOpusChannels(connection *discordgo.VoiceConnection, timeout time.Duration) error {
	startTime := time.Now()
	pollInterval := 50 * time.Millisecond

	d.logger.Debug("DISCORD_CONN", "Waiting for voice connection to become ready")

	var opusSend chan []byte
	var opusRecv chan *discordgo.Packet

	// Wait for Ready=true AND opus channels to be non-nil (all checked atomically)
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

	// Store channels ONCE and never access VoiceConnection fields again
	d.connMutex.Lock()
	d.connection = connection
	d.opusSend = opusSend
	d.opusRecv = opusRecv
	atomic.AddInt32(&d.connectionGeneration, 1) // Increment generation for stale reference detection
	d.connMutex.Unlock()
	d.logger.Debug("DISCORD_CONN", "Opus channels successfully extracted")

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

// isConnectionHealthy checks if the connection is healthy
// This checks both our stored channel references and the VoiceConnection.Ready flag
func (d *DiscordVoiceConnectionManager) isConnectionHealthy() bool {
	d.connMutex.RLock()
	defer d.connMutex.RUnlock()

	// Check that channel references exist - do NOT read from channels
	// Reading from opusRecv would discard audio packets causing data loss
	if d.opusSend == nil || d.opusRecv == nil || d.connection == nil {
		return false
	}

	// Also check VoiceConnection.Ready with proper locking
	// This flag indicates the voice WebSocket connection is active
	d.connection.RLock()
	ready := d.connection.Ready
	d.connection.RUnlock()

	return ready
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

	// Remove voice event handlers
	d.removeVoiceEventHandlers()

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
