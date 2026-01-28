// Package bridge provides the core bridge functionality for connecting Mumble and Discord.
package bridge

import (
	"context"
	"crypto/tls"
	"fmt"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/stieneee/gumble/gumble"
	"github.com/stieneee/mumble-discord-bridge/pkg/logger"
)

// DiscordUser represents a Discord user in the bridge.
type DiscordUser struct {
	username string
	seen     bool
	dm       *discordgo.Channel
}

// BridgeMode represents the operational mode of the bridge.
type BridgeMode int //nolint:revive // API consistency: keeping Bridge prefix for public types

// define a String method for the BridgeMode type
func (b BridgeMode) String() string {
	return [...]string{"auto", "manual", "constant"}[b]
}

const (
	// BridgeModeAuto automatically starts/stops bridge based on user presence.
	BridgeModeAuto BridgeMode = iota
	// BridgeModeManual requires manual start/stop control.
	BridgeModeManual
	// BridgeModeConstant keeps the bridge always running.
	BridgeModeConstant
)

// BridgeConfig holds the configuration for a bridge instance.
type BridgeConfig struct { //nolint:revive // API consistency: keeping Bridge prefix for public types
	// The command prefix for the bot
	Command string

	// The mumble server configuration
	MumbleConfig *gumble.Config

	// The mumble server address
	MumbleAddr string

	// The mumble server certificate
	MumbleInsecure bool

	// The mumble server certificate
	MumbleCertificate string

	// The mumble channel to join - it has been parse to an array of strings representing the channel path
	MumbleChannel []string

	// The mumble voice stream count
	MumbleStartStreamCount int

	// Disable text messages to mumble
	MumbleDisableText bool

	// Respond to mumble commands
	MumbleCommand bool

	// Mumble bot flag
	MumbleBotFlag bool

	// The discord server ID
	GID string

	// The discord voice channel ID
	CID string

	// The discord voice stream count
	DiscordStartStreamingCount int

	// The discord text mode, channel, user, disabled
	DiscordTextMode string

	// Respond to discord commands
	DiscordCommand bool

	// Bridge messages between mumble and discord
	ChatBridge bool

	// The version of the bridge
	Version string
}

// BridgeState manages dynamic information about the bridge during runtime
//
// CONCURRENCY NOTES:
//   - BridgeMutex protects: Connected, DiscordConnected, MumbleConnected, Mode,
//     MumbleClient, DiscordVoice, StartTime
//   - DiscordUsersMutex protects: DiscordUsers map
//   - MumbleUsersMutex protects: MumbleUsers map, MumbleUserCount
//   - Lock order: BridgeMutex -> MumbleUsersMutex -> DiscordUsersMutex
//
// BridgeState manages dynamic information about the bridge during runtime.
type BridgeState struct { //nolint:revive // API consistency: keeping Bridge prefix for public types
	// The configuration data for this bridge
	BridgeConfig *BridgeConfig

	// External requests to kill the bridge
	BridgeDie chan bool

	// Lock to only allow one bridge session at a time
	lock sync.Mutex

	// Wait for bridge to exit cleanly
	WaitExit *sync.WaitGroup

	// Bridge State Mutex
	BridgeMutex sync.Mutex

	// Bridge connection status - now tracks overall bridge state
	Connected bool

	// Individual connection states
	DiscordConnected bool
	MumbleConnected  bool

	// The bridge mode constant, auto, manual. Default is constant.
	Mode BridgeMode

	// Discord session. This is created and outside the bridge state
	DiscordSession *discordgo.Session

	// Connection managers for smart connection handling
	DiscordVoiceConnectionManager *DiscordVoiceConnectionManager
	MumbleConnectionManager       *MumbleConnectionManager

	// Map of Discord users tracked by this bridge.
	DiscordUsers      map[string]DiscordUser
	DiscordUsersMutex sync.Mutex

	// Map of Mumble users tracked by this bridge
	MumbleUsers      map[string]bool
	MumbleUsersMutex sync.Mutex

	// Total Number of Mumble users
	MumbleUserCount int

	// Kill the auto connect routine
	AutoChanDie chan bool

	// Discord Duplex and Event Listener
	DiscordStream   *DiscordDuplex
	DiscordListener *DiscordListener

	// Mumble Duplex and Event Listener
	MumbleStream   *MumbleDuplex
	MumbleListener *MumbleListener

	// Discord Voice channel to join
	DiscordChannelID string

	// Start time of the bridge
	StartTime time.Time

	// Logger for this bridge
	Logger logger.Logger

	// Metrics change callback for event-driven updates
	MetricsChangeCallback func()

	// Connection event handling context and cancellation
	connectionCtx    context.Context
	connectionCancel context.CancelFunc

	// Reference to BridgeInstance for event forwarding (if available)
	BridgeInstance interface {
		EmitConnectionEvent(service string, eventType int, connected bool, err error)
		EmitUserEvent(service string, eventType int, username string, err error)
	}
}

// notifyMetricsChange triggers the metrics change callback if it's set
func (b *BridgeState) notifyMetricsChange() {
	if b.MetricsChangeCallback != nil {
		// Run callback in a goroutine to avoid blocking the event handler
		go b.MetricsChangeCallback()
	}
}

// EmitConnectionEvent implements BridgeEventEmitter interface
func (b *BridgeState) EmitConnectionEvent(service string, eventType int, connected bool, err error) {
	// Forward the event to BridgeInstance if available (for bridgelib integration)
	if b.BridgeInstance != nil {
		b.BridgeInstance.EmitConnectionEvent(service, eventType, connected, err)
	}

	// Also update internal connection state for compatibility
	b.BridgeMutex.Lock()
	defer b.BridgeMutex.Unlock()

	switch service {
	case "discord":
		b.DiscordConnected = connected
		if connected {
			b.Logger.Info("BRIDGE", "Discord connected via connection manager")
		} else {
			b.Logger.Info("BRIDGE", "Discord disconnected via connection manager")
			if err != nil {
				b.Logger.Error("BRIDGE", fmt.Sprintf("Discord disconnection error: %v", err))
			}
		}
	case "mumble":
		b.MumbleConnected = connected
		if connected {
			b.Logger.Info("BRIDGE", "Mumble connected via connection manager")
		} else {
			b.Logger.Info("BRIDGE", "Mumble disconnected via connection manager")
			if err != nil {
				b.Logger.Error("BRIDGE", fmt.Sprintf("Mumble disconnection error: %v", err))
			}
		}
	}

	// Update overall connection state
	b.Connected = b.DiscordConnected && b.MumbleConnected

	// Notify metrics change for event-driven updates
	b.notifyMetricsChange()
}

// EmitUserEvent emits user join/leave events to BridgeInstance
func (b *BridgeState) EmitUserEvent(service string, eventType int, username string, err error) {
	// Forward the event to BridgeInstance if available (for bridgelib integration)
	if b.BridgeInstance != nil {
		b.BridgeInstance.EmitUserEvent(service, eventType, username, err)
	}
}

// initializeConnectionManagers creates and initializes the connection managers
func (b *BridgeState) initializeConnectionManagers() error {
	b.Logger.Debug("BRIDGE", "Initializing connection managers")

	// Create connection context
	b.connectionCtx, b.connectionCancel = context.WithCancel(context.Background())

	// Initialize Discord connection manager
	if b.DiscordSession != nil && b.DiscordChannelID != "" {
		b.DiscordVoiceConnectionManager = NewDiscordVoiceConnectionManager(
			b.DiscordSession,
			b.BridgeConfig.GID,
			b.DiscordChannelID,
			b.Logger,
			b,
		)
		b.Logger.Debug("BRIDGE", "Discord connection manager initialized")
	} else {
		return fmt.Errorf("discord session or channel ID not available")
	}

	// Initialize Mumble connection manager
	if b.BridgeConfig.MumbleConfig != nil {
		// Configure TLS settings
		var tlsConfig tls.Config
		if b.BridgeConfig.MumbleInsecure {
			tlsConfig.InsecureSkipVerify = true // nolint: gosec // Intentionally insecure for testing
		}

		if b.BridgeConfig.MumbleCertificate != "" {
			keyFile := b.BridgeConfig.MumbleCertificate
			certificate, err := tls.LoadX509KeyPair(keyFile, keyFile)
			if err != nil {
				return fmt.Errorf("failed to load Mumble client certificate %s: %w", keyFile, err)
			}
			tlsConfig.Certificates = append(tlsConfig.Certificates, certificate)
		}

		b.MumbleConnectionManager = NewMumbleConnectionManager(
			b.BridgeConfig.MumbleAddr,
			b.BridgeConfig.MumbleConfig,
			&tlsConfig,
			b.Logger,
			b, // BridgeState implements BridgeEventEmitter
		)
		b.Logger.Debug("BRIDGE", "Mumble connection manager initialized")
	} else {
		return fmt.Errorf("mumble config not available")
	}

	return nil
}

// startConnectionManagers starts both connection managers and begins monitoring
func (b *BridgeState) startConnectionManagers() error {
	b.Logger.Info("BRIDGE", "Starting connection managers")

	// Start Discord connection manager
	if err := b.DiscordVoiceConnectionManager.Start(b.connectionCtx); err != nil {
		return fmt.Errorf("failed to start Discord connection manager: %w", err)
	}

	// Start Mumble connection manager
	if err := b.MumbleConnectionManager.Start(b.connectionCtx); err != nil {
		return fmt.Errorf("failed to start Mumble connection manager: %w", err)
	}

	// Start connection event monitoring
	go b.monitorConnectionEvents()

	// Start metrics updater for connection uptimes
	go b.updateConnectionMetrics()

	b.Logger.Info("BRIDGE", "Connection managers started successfully")

	return nil
}

// monitorConnectionEvents monitors connection events from both managers
func (b *BridgeState) monitorConnectionEvents() {
	b.Logger.Debug("BRIDGE", "Starting connection event monitoring")

	defer func() {
		if r := recover(); r != nil {
			b.Logger.Error("BRIDGE", fmt.Sprintf("Connection event monitoring panic recovered: %v", r))
			// Restart monitoring after a brief delay if context is still active
			if b.connectionCtx.Err() == nil {
				go func() {
					time.Sleep(5 * time.Second)
					b.monitorConnectionEvents()
				}()
			}
		}
	}()

	for {
		select {
		case <-b.connectionCtx.Done():
			b.Logger.Debug("BRIDGE", "Connection event monitoring stopped")

			return

		case event, ok := <-b.DiscordVoiceConnectionManager.GetEventChannel():
			if !ok {
				b.Logger.Warn("BRIDGE", "Discord connection event channel closed")

				return
			}
			b.handleDiscordConnectionEvent(event)

		case event, ok := <-b.MumbleConnectionManager.GetEventChannel():
			if !ok {
				b.Logger.Warn("BRIDGE", "Mumble connection event channel closed")

				return
			}
			b.handleMumbleConnectionEvent(event)
		}
	}
}

// handleDiscordConnectionEvent handles Discord connection events
func (b *BridgeState) handleDiscordConnectionEvent(event ConnectionEvent) {
	// Only log non-health-check events to avoid spam
	if event.Type != EventHealthCheck {
		b.Logger.Debug("BRIDGE", fmt.Sprintf("Discord connection event: %s (status: %s)", event.Type, event.Status))
	}

	// Update metrics for connection status
	promDiscordConnectionStatus.Set(float64(event.Status))
	promConnectionManagerEvents.WithLabelValues("discord", fmt.Sprintf("%d", int(event.Type))).Inc()

	// Track reconnection attempts
	if event.Type == EventReconnecting {
		promDiscordReconnectAttempts.Inc()
	}

	b.BridgeMutex.Lock()
	oldState := b.DiscordConnected
	newConnectedState := event.Status == ConnectionConnected

	// Update connection state atomically
	b.DiscordConnected = newConnectedState

	b.updateOverallConnectionState()
	newState := b.DiscordConnected
	b.BridgeMutex.Unlock()

	if oldState != newState {
		b.Logger.Info("BRIDGE", fmt.Sprintf("Discord connection state changed: %v -> %v", oldState, newState))

		// When Discord connects, populate existing users in the voice channel
		if newState && !oldState {
			go b.populateExistingDiscordUsers()

			// Force immediate Discord user count metric update
			go func() {
				b.DiscordUsersMutex.Lock()
				userCount := len(b.DiscordUsers)
				b.DiscordUsersMutex.Unlock()
				promDiscordUsers.Set(float64(userCount))
				b.Logger.Debug("BRIDGE", fmt.Sprintf("Forced Discord user count metric update: %d users", userCount))
			}()
		}

		b.notifyMetricsChange()
	}

	if event.Error != nil {
		b.Logger.Error("BRIDGE", fmt.Sprintf("Discord connection error: %v", event.Error))
	}
}

// handleMumbleConnectionEvent handles Mumble connection events
func (b *BridgeState) handleMumbleConnectionEvent(event ConnectionEvent) {
	// Only log non-health-check events to avoid spam
	if event.Type != EventHealthCheck {
		b.Logger.Debug("BRIDGE", fmt.Sprintf("Mumble connection event: %s (status: %s)", event.Type, event.Status))
	}

	// Update metrics for connection status
	promMumbleConnectionStatus.Set(float64(event.Status))
	promConnectionManagerEvents.WithLabelValues("mumble", fmt.Sprintf("%d", int(event.Type))).Inc()

	// Track reconnection attempts
	if event.Type == EventReconnecting {
		promMumbleReconnectAttempts.Inc()
	}

	b.BridgeMutex.Lock()
	oldState := b.MumbleConnected
	newConnectedState := event.Status == ConnectionConnected

	// Update connection state atomically
	b.MumbleConnected = newConnectedState

	b.updateOverallConnectionState()
	newState := b.MumbleConnected
	b.BridgeMutex.Unlock()

	if oldState != newState {
		b.Logger.Info("BRIDGE", fmt.Sprintf("Mumble connection state changed: %v -> %v", oldState, newState))
		b.notifyMetricsChange()
	}

	if event.Error != nil {
		b.Logger.Error("BRIDGE", fmt.Sprintf("Mumble connection error: %v", event.Error))
	}
}

// updateOverallConnectionState updates the overall bridge connection state based on bridge mode
// This version requires the caller to hold the BridgeMutex
func (b *BridgeState) updateOverallConnectionState() {
	// Bridge is considered connected if both connections are up
	// or if in auto mode and at least one connection is up
	oldConnected := b.Connected

	switch b.Mode {
	case BridgeModeConstant:
		// Constant mode: bridge is running regardless of connection states
		b.Connected = true
	case BridgeModeAuto:
		// Auto mode: bridge is connected if at least one connection is up
		b.Connected = b.DiscordConnected || b.MumbleConnected
	case BridgeModeManual:
		// Manual mode: bridge is connected if both connections are up
		b.Connected = b.DiscordConnected && b.MumbleConnected
	default:
		// Default to constant mode behavior
		b.Connected = true
	}

	if oldConnected != b.Connected {
		b.Logger.Info("BRIDGE", fmt.Sprintf("Overall bridge connection state changed: %v -> %v (Discord: %v, Mumble: %v)",
			oldConnected, b.Connected, b.DiscordConnected, b.MumbleConnected))
	}
}

// UpdateOverallConnectionState is the public thread-safe version
func (b *BridgeState) UpdateOverallConnectionState() {
	b.BridgeMutex.Lock()
	defer b.BridgeMutex.Unlock()
	b.updateOverallConnectionState()
}

// stopConnectionManagers stops both connection managers
func (b *BridgeState) stopConnectionManagers() {
	b.Logger.Info("BRIDGE", "Stopping connection managers")

	if b.connectionCancel != nil {
		b.connectionCancel()
	}

	if b.DiscordVoiceConnectionManager != nil {
		if err := b.DiscordVoiceConnectionManager.Stop(); err != nil {
			b.Logger.Error("BRIDGE", fmt.Sprintf("Error stopping Discord connection manager: %v", err))
		}
	}

	if b.MumbleConnectionManager != nil {
		if err := b.MumbleConnectionManager.Stop(); err != nil {
			b.Logger.Error("BRIDGE", fmt.Sprintf("Error stopping Mumble connection manager: %v", err))
		}
	}

	b.BridgeMutex.Lock()
	b.DiscordConnected = false
	b.MumbleConnected = false
	b.Connected = false
	// Connection manager cleanup handles all cleanup
	b.BridgeMutex.Unlock()

	b.Logger.Info("BRIDGE", "Connection managers stopped")
}

// forwardToMumble manages the connection to Mumble's audio channel
// It properly handles channel lifecycle and reconnections
func (b *BridgeState) forwardToMumble(ctx context.Context, internalChan <-chan gumble.AudioBuffer) {
	var mu sync.Mutex
	var mumbleOutgoing chan<- gumble.AudioBuffer
	var lastConnectionState bool
	var connectionGeneration int64

	// safeClose closes a channel with panic recovery for double-close protection
	safeClose := func(ch chan<- gumble.AudioBuffer, gen int64) {
		if ch == nil {
			return
		}
		defer func() {
			if r := recover(); r != nil {
				b.Logger.Debug("MUMBLE_FORWARDER", fmt.Sprintf("Channel close recovered (gen %d): %v", gen, r))
			}
		}()
		close(ch)
		b.Logger.Debug("MUMBLE_FORWARDER", fmt.Sprintf("Closed Mumble audio channel (gen %d)", gen))
	}

	// Cleanup function to close Mumble channel and reset state
	cleanup := func() {
		mu.Lock()
		defer mu.Unlock()
		if mumbleOutgoing != nil {
			safeClose(mumbleOutgoing, connectionGeneration)
			mumbleOutgoing = nil
		}
	}

	defer cleanup()

	b.Logger.Debug("MUMBLE_FORWARDER", "Starting Mumble audio forwarder")

	for {
		select {
		case <-ctx.Done():
			b.Logger.Debug("MUMBLE_FORWARDER", "Stopping Mumble audio forwarder")

			return

		case packet := <-internalChan:
			// Check current connection state
			currentConnectionState := b.MumbleConnectionManager != nil && b.MumbleConnectionManager.IsConnected()

			// Handle connection state changes
			if currentConnectionState != lastConnectionState {
				mu.Lock()
				if currentConnectionState {
					// Mumble just connected
					b.Logger.Debug("MUMBLE_FORWARDER", "Mumble connected, creating audio channel")

					// Close old channel if any (with old generation)
					if mumbleOutgoing != nil {
						safeClose(mumbleOutgoing, connectionGeneration)
						mumbleOutgoing = nil
					}

					// Increment generation for new connection
					connectionGeneration++
					currentGen := connectionGeneration

					// Get new channel for this connection session
					if b.MumbleConnectionManager != nil {
						mumbleOutgoing = b.MumbleConnectionManager.GetAudioOutgoing()
						if mumbleOutgoing != nil {
							b.Logger.Debug("MUMBLE_FORWARDER", fmt.Sprintf("Created audio channel (gen %d)", currentGen))
						}
					}
				} else {
					// Mumble disconnected
					b.Logger.Debug("MUMBLE_FORWARDER", "Mumble disconnected, cleaning up audio channel")
					if mumbleOutgoing != nil {
						safeClose(mumbleOutgoing, connectionGeneration)
						mumbleOutgoing = nil
					}
				}
				lastConnectionState = currentConnectionState
				mu.Unlock()
			}

			// Forward packet if we have a valid channel
			// Check connection state and channel together under the same lock to avoid TOCTOU race
			mu.Lock()
			outChan := mumbleOutgoing
			isConnected := b.MumbleConnectionManager != nil && b.MumbleConnectionManager.IsConnected()
			mu.Unlock()

			if outChan != nil && isConnected {
				select {
				case outChan <- packet:
					promSentMumblePackets.Inc()
				default:
					// Channel full, drop packet
					b.Logger.Debug("MUMBLE_FORWARDER", "Mumble channel full, dropping packet")
					promToMumbleDropped.Inc()
				}
			} else {
				// Not connected, sink packet
				promPacketsSunk.WithLabelValues("mumble", "inbound").Inc()
			}
		}
	}
}

// populateExistingDiscordUsers populates the DiscordUsers map with users already in the voice channel
func (b *BridgeState) populateExistingDiscordUsers() {
	b.Logger.Debug("BRIDGE", "Populating existing Discord users")

	if b.DiscordSession == nil || b.DiscordChannelID == "" || b.BridgeConfig.GID == "" {
		b.Logger.Debug("BRIDGE", "Cannot populate users - missing Discord session, channel ID, or guild ID")

		return
	}

	// Get the guild
	guild, err := b.DiscordSession.State.Guild(b.BridgeConfig.GID)
	if err != nil {
		b.Logger.Debug("BRIDGE", fmt.Sprintf("Could not get guild from state, trying API: %v", err))
		guild, err = b.DiscordSession.Guild(b.BridgeConfig.GID)
		if err != nil {
			b.Logger.Error("BRIDGE", fmt.Sprintf("Could not get guild: %v", err))

			return
		}
	}

	b.Logger.Debug("BRIDGE", fmt.Sprintf("Found guild %s with %d voice states", guild.Name, len(guild.VoiceStates)))

	// Collect new users to add (avoid deadlock by not holding Discord lock when acquiring Bridge lock)
	type newUser struct {
		userID   string
		username string
		dm       *discordgo.Channel
	}
	var newUsers []newUser
	var notifications []string

	// First pass: collect user information without holding Discord users lock
	for _, vs := range guild.VoiceStates {
		if vs.ChannelID == b.DiscordChannelID {
			if b.DiscordSession.State.User.ID == vs.UserID {
				// Ignore bot
				continue
			}

			// Check if user is already tracked (quick check with lock)
			b.DiscordUsersMutex.Lock()
			_, exists := b.DiscordUsers[vs.UserID]
			b.DiscordUsersMutex.Unlock()

			if exists {
				b.Logger.Debug("BRIDGE", fmt.Sprintf("User %s already tracked", vs.UserID))

				continue
			}

			// Get user information
			user, err := b.DiscordSession.User(vs.UserID)
			if err != nil {
				b.Logger.Error("BRIDGE", fmt.Sprintf("Error looking up username for %s: %v", vs.UserID, err))

				continue
			}

			// Create DM channel
			dm, err := b.DiscordSession.UserChannelCreate(user.ID)
			if err != nil {
				b.Logger.Error("BRIDGE", fmt.Sprintf("Error creating private channel for %s: %v", user.Username, err))
			}

			// Store for later addition
			newUsers = append(newUsers, newUser{
				userID:   vs.UserID,
				username: user.Username,
				dm:       dm,
			})

			b.Logger.Info("BRIDGE", fmt.Sprintf("Found existing Discord user: %s", user.Username))
			notifications = append(notifications, user.Username)
		}
	}

	// Second pass: add users to tracking map
	if len(newUsers) > 0 {
		b.DiscordUsersMutex.Lock()
		for _, nu := range newUsers {
			// Double-check user wasn't added by another goroutine
			if _, exists := b.DiscordUsers[nu.userID]; !exists {
				b.DiscordUsers[nu.userID] = DiscordUser{
					username: nu.username,
					seen:     true,
					dm:       nu.dm,
				}
			}
		}
		userCount := len(b.DiscordUsers)
		b.DiscordUsersMutex.Unlock()

		b.Logger.Info("BRIDGE", fmt.Sprintf("Populated %d existing Discord users", len(newUsers)))
		promDiscordUsers.Set(float64(userCount))
		b.notifyMetricsChange()

		// Send notifications asynchronously to avoid blocking
		if len(notifications) > 0 {
			go b.sendMumbleNotifications(notifications)
		}
	} else {
		b.Logger.Debug("BRIDGE", "No existing Discord users found in voice channel")
	}
}

// sendMumbleNotifications sends notifications to Mumble with timeout protection
func (b *BridgeState) sendMumbleNotifications(usernames []string) {
	b.BridgeMutex.Lock()
	connected := b.Connected
	disableText := b.BridgeConfig.MumbleDisableText
	b.BridgeMutex.Unlock()

	// Get current Mumble client from connection manager
	var client *gumble.Client
	if b.MumbleConnectionManager != nil {
		client = b.MumbleConnectionManager.GetClient()
	}

	if !connected || disableText || client == nil {
		return
	}

	for _, username := range usernames {
		// Use a timeout channel to prevent blocking indefinitely
		done := make(chan bool, 1)
		go func(name string) {
			defer func() {
				if r := recover(); r != nil {
					b.Logger.Error("BRIDGE", fmt.Sprintf("Panic in Mumble notification: %v", r))
				}
				select {
				case done <- true:
				default:
				}
			}()

			client.Do(func() {
				if client.Self != nil && client.Self.Channel != nil {
					client.Self.Channel.Send(fmt.Sprintf("%v was already in Discord\n", name), false)
				}
			})
		}(username)

		// Wait for completion with timeout
		select {
		case <-done:
			b.Logger.Debug("BRIDGE", fmt.Sprintf("Sent Mumble notification for %s", username))
		case <-time.After(2 * time.Second):
			b.Logger.Warn("BRIDGE", fmt.Sprintf("Timeout sending Mumble notification for %s", username))
		}
	}
}

// updateConnectionMetrics periodically updates connection uptime metrics
func (b *BridgeState) updateConnectionMetrics() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	var discordConnectedSince time.Time
	var mumbleConnectedSince time.Time

	for {
		select {
		case <-b.connectionCtx.Done():
			return
		case <-ticker.C:
			b.BridgeMutex.Lock()
			discordConnected := b.DiscordConnected
			mumbleConnected := b.MumbleConnected
			b.BridgeMutex.Unlock()

			// Track Discord connection uptime
			if discordConnected {
				if discordConnectedSince.IsZero() {
					discordConnectedSince = time.Now()
				}
				uptime := time.Since(discordConnectedSince).Seconds()
				promDiscordConnectionUptime.Set(uptime)
			} else {
				discordConnectedSince = time.Time{}
				promDiscordConnectionUptime.Set(0)
			}

			// Track Mumble connection uptime
			if mumbleConnected {
				if mumbleConnectedSince.IsZero() {
					mumbleConnectedSince = time.Now()
				}
				uptime := time.Since(mumbleConnectedSince).Seconds()
				promMumbleConnectionUptime.Set(uptime)
			} else {
				mumbleConnectedSince = time.Time{}
				promMumbleConnectionUptime.Set(0)
			}
		}
	}
}

// StartBridge establishes the voice bridge using managed connections
func (b *BridgeState) StartBridge() {
	b.Logger.Debug("BRIDGE", "StartBridge called, checking connection status")

	b.BridgeMutex.Lock()
	if b.Connected {
		b.Logger.Info("BRIDGE", "Bridge already connected, aborting start")
		b.BridgeMutex.Unlock()

		return
	}
	// Set StartTime while holding lock to prevent races
	b.StartTime = time.Now()
	b.BridgeMutex.Unlock()

	b.Logger.Info("BRIDGE", "Starting bridge process with managed connections")

	b.lock.Lock()
	defer b.lock.Unlock()

	b.BridgeDie = make(chan bool)
	defer close(b.BridgeDie)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	wg := sync.WaitGroup{}
	b.WaitExit = &wg

	promBridgeStarts.Inc()
	promBridgeStartTime.SetToCurrentTime()

	// Initialize connection managers
	if err := b.initializeConnectionManagers(); err != nil {
		b.Logger.Error("BRIDGE", fmt.Sprintf("Failed to initialize connection managers: %v", err))

		return
	}

	// Start connection managers
	if err := b.startConnectionManagers(); err != nil {
		b.Logger.Error("BRIDGE", fmt.Sprintf("Failed to start connection managers: %v", err))
		b.stopConnectionManagers()

		return
	}

	// Set up audio streams
	b.MumbleStream = NewMumbleDuplex(b.Logger, b)
	b.DiscordStream = NewDiscordDuplex(b)

	// Start Discord stream cleanup goroutine to prevent memory leaks
	b.DiscordStream.StartCleanup(ctx)

	// Attach Mumble audio listener to config (like original code)
	// This ensures the listener is attached when the client connects
	var det gumble.Detacher
	if b.BridgeConfig.MumbleConfig != nil {
		det = b.BridgeConfig.MumbleConfig.AudioListeners.Attach(b.MumbleStream)
	}

	// Ensure proper cleanup order: detach audio listener before stopping connection managers
	defer func() {
		// First detach audio listener to prevent race with ongoing audio processing
		if det != nil {
			b.Logger.Debug("BRIDGE", "Detaching Mumble audio listener")
			det.Detach()
			// Small delay to allow any in-flight audio processing to complete
			time.Sleep(50 * time.Millisecond)
		}

		// Stop Discord stream cleanup goroutine
		if b.DiscordStream != nil {
			b.DiscordStream.StopCleanup()
		}

		// Clean up any active audio streams
		if b.MumbleStream != nil {
			b.MumbleStream.CleanupStreams()
		}

		// Then stop connection managers
		b.stopConnectionManagers()
	}()

	// Set up audio channels with proper lifecycle management
	toMumbleInternal := make(chan gumble.AudioBuffer, 100)
	toDiscord := make(chan []int16, 200)
	defer close(toMumbleInternal)
	defer close(toDiscord)

	// Audio routing goroutines
	// From Mumble to Discord
	wg.Add(1)
	go func() {
		defer wg.Done()
		b.MumbleStream.fromMumbleMixer(ctx, toDiscord)
	}()

	// Discord receive PCM
	wg.Add(1)
	go func() {
		defer wg.Done()
		b.DiscordStream.discordReceivePCM(ctx)
	}()

	// From Discord to Mumble (via internal channel)
	wg.Add(1)
	go func() {
		defer wg.Done()
		b.DiscordStream.fromDiscordMixer(ctx, toMumbleInternal)
	}()

	// Mumble audio forwarder - manages connection to actual Mumble channel
	wg.Add(1)
	go func() {
		defer wg.Done()
		b.forwardToMumble(ctx, toMumbleInternal)
	}()

	// Discord send PCM
	wg.Add(1)
	go func() {
		defer wg.Done()
		b.DiscordStream.discordSendPCM(ctx, toDiscord)
	}()

	// Bridge health monitor - checks overall bridge state but doesn't kill on individual connection failures
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				b.BridgeMutex.Lock()
				discordConnected := b.DiscordConnected
				mumbleConnected := b.MumbleConnected
				mode := b.Mode
				b.BridgeMutex.Unlock()

				// In auto mode, check if we should stop the bridge due to no users
				if mode == BridgeModeAuto {
					if !discordConnected && !mumbleConnected {
						b.Logger.Info("BRIDGE", "Both connections lost in auto mode, stopping bridge")
						cancel()

						return
					}
				}

				// Health check completed - no periodic logging

			case <-ctx.Done():
				return
			}
		}
	}()

	// Mark bridge as connected (overall state)
	b.BridgeMutex.Lock()
	b.Connected = true
	b.BridgeMutex.Unlock()

	b.notifyMetricsChange()
	b.Logger.Info("BRIDGE", "Bridge started with managed connections")

	// Hold until canceled or external die request
	select {
	case <-ctx.Done():
		b.Logger.Debug("BRIDGE", "Bridge internal context cancel")
	case <-b.BridgeDie:
		b.Logger.Debug("BRIDGE", "Bridge die request received")
		cancel()
	}

	b.BridgeMutex.Lock()
	b.Connected = false
	b.BridgeMutex.Unlock()

	b.notifyMetricsChange()

	wg.Wait()
	b.Logger.Info("BRIDGE", "Terminating Bridge")

	// Clean up user tracking
	b.MumbleUsersMutex.Lock()
	b.MumbleUsers = make(map[string]bool)
	b.MumbleUsersMutex.Unlock()
	b.DiscordUsersMutex.Lock()
	b.DiscordUsers = make(map[string]DiscordUser)
	b.DiscordUsersMutex.Unlock()
}

// StopBridge gracefully stops the bridge
func (b *BridgeState) StopBridge() {
	b.Logger.Info("BRIDGE", "StopBridge called, initiating graceful shutdown")

	// Signal bridge to stop
	select {
	case b.BridgeDie <- true:
		b.Logger.Debug("BRIDGE", "Bridge stop signal sent")
	default:
		b.Logger.Debug("BRIDGE", "Bridge stop signal channel full or closed")
	}

	// Wait for bridge to exit cleanly if WaitExit is available
	if b.WaitExit != nil {
		b.Logger.Debug("BRIDGE", "Waiting for bridge to exit cleanly")
		b.WaitExit.Wait()
		b.Logger.Debug("BRIDGE", "Bridge exited cleanly")
	}
}

// IsConnected returns true if the bridge is considered connected
func (b *BridgeState) IsConnected() bool {
	b.BridgeMutex.Lock()
	defer b.BridgeMutex.Unlock()

	return b.Connected
}

// GetConnectionStates returns the current connection states
func (b *BridgeState) GetConnectionStates() (discord, mumble, overall bool) {
	b.BridgeMutex.Lock()
	defer b.BridgeMutex.Unlock()

	return b.DiscordConnected, b.MumbleConnected, b.Connected
}

// MumblePingLoop periodically pings the Mumble server to update user count for auto mode.
// This is a per-bridge operation needed for auto mode to detect when to connect.
func (b *BridgeState) MumblePingLoop(ctx context.Context) {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	b.Logger.Info("BRIDGE", "Starting Mumble ping loop for auto mode")

	for {
		select {
		case <-ctx.Done():
			b.Logger.Info("BRIDGE", "Mumble ping loop stopped")

			return
		case <-ticker.C:
			resp, err := gumble.Ping(b.BridgeConfig.MumbleAddr, -1, 30*time.Second)
			if err != nil {
				b.Logger.Error("BRIDGE", fmt.Sprintf("Error pinging mumble server: %v", err))

				continue
			}

			promMumblePing.Set(float64(resp.Ping.Milliseconds()))

			// Use consistent lock ordering: BridgeMutex -> MumbleUsersMutex
			b.BridgeMutex.Lock()
			b.MumbleUsersMutex.Lock()
			b.MumbleUserCount = resp.ConnectedUsers
			if b.Connected {
				b.MumbleUserCount--
			}
			b.MumbleUsersMutex.Unlock()
			b.BridgeMutex.Unlock()
		}
	}
}

// PopulateExistingDiscordUsers checks the Discord voice channel for existing users.
// This is needed because GuildCreate may fire before handlers are registered.
func (b *BridgeState) PopulateExistingDiscordUsers() {
	b.Logger.Debug("BRIDGE", "Populating existing Discord voice users")

	session := b.DiscordSession
	if session == nil || session.State == nil {
		b.Logger.Warn("BRIDGE", "Discord session not ready, skipping user population")

		return
	}

	session.State.RLock()
	guild, err := session.State.Guild(b.BridgeConfig.GID)
	if err != nil {
		session.State.RUnlock()
		b.Logger.Warn("BRIDGE", fmt.Sprintf("Could not get guild state: %v", err))

		return
	}

	var voiceStates []*discordgo.VoiceState
	if guild.VoiceStates != nil {
		voiceStates = make([]*discordgo.VoiceState, len(guild.VoiceStates))
		copy(voiceStates, guild.VoiceStates)
	}
	session.State.RUnlock()

	count := 0
	b.DiscordUsersMutex.Lock()
	for _, vs := range voiceStates {
		if vs.ChannelID == b.DiscordChannelID {
			if session.State.User != nil && session.State.User.ID == vs.UserID {
				// Ignore bot
				continue
			}

			if _, exists := b.DiscordUsers[vs.UserID]; !exists {
				u, err := session.User(vs.UserID)
				if err != nil {
					b.Logger.Error("BRIDGE", fmt.Sprintf("Error looking up user %s: %v", vs.UserID, err))

					continue
				}

				b.Logger.Info("BRIDGE", fmt.Sprintf("Found existing Discord user: %s", u.Username))
				dm, err := session.UserChannelCreate(u.ID)
				if err != nil {
					b.Logger.Error("BRIDGE", fmt.Sprintf("Error creating DM channel for %s: %v", u.Username, err))
				}
				b.DiscordUsers[vs.UserID] = DiscordUser{
					username: u.Username,
					seen:     true,
					dm:       dm,
				}
				count++
			}
		}
	}
	b.DiscordUsersMutex.Unlock()

	b.Logger.Info("BRIDGE", fmt.Sprintf("Populated %d existing Discord users", count))
}

// refreshDiscordVoiceUsers checks the Discord session state for users in the voice channel.
// This is called periodically in auto mode to handle the case where GuildCreate
// fires after initial startup (due to async event timing).
func (b *BridgeState) refreshDiscordVoiceUsers() {
	session := b.DiscordSession
	if session == nil || session.State == nil {
		return
	}

	session.State.RLock()
	guild, err := session.State.Guild(b.BridgeConfig.GID)
	if err != nil {
		session.State.RUnlock()

		return
	}

	var voiceStates []*discordgo.VoiceState
	if guild.VoiceStates != nil {
		voiceStates = make([]*discordgo.VoiceState, len(guild.VoiceStates))
		copy(voiceStates, guild.VoiceStates)
	}
	session.State.RUnlock()

	b.DiscordUsersMutex.Lock()
	for _, vs := range voiceStates {
		if vs.ChannelID == b.DiscordChannelID {
			if session.State.User != nil && session.State.User.ID == vs.UserID {
				continue // Skip bot
			}

			if _, exists := b.DiscordUsers[vs.UserID]; !exists {
				u, err := session.User(vs.UserID)
				if err != nil {
					continue
				}

				b.Logger.Info("BRIDGE", fmt.Sprintf("Auto mode detected Discord user: %s", u.Username))
				dm, err := session.UserChannelCreate(u.ID)
				if err != nil {
					b.Logger.Error("BRIDGE", fmt.Sprintf("Error creating DM channel for user %s: %v", u.Username, err))
				}
				b.DiscordUsers[vs.UserID] = DiscordUser{
					username: u.Username,
					seen:     true,
					dm:       dm,
				}
			}
		}
	}
	b.DiscordUsersMutex.Unlock()
}

// AutoBridge starts a goroutine to check the number of users in discord and mumble
// when there is at least one user on both, starts up the bridge
// when there are no users on either side, kills the bridge
func (b *BridgeState) AutoBridge() {
	b.Logger.Info("BRIDGE", "Beginning auto mode with managed connections")
	ticker := time.NewTicker(3 * time.Second)

	for {
		select {
		case <-ticker.C:
		case <-b.AutoChanDie:
			b.Logger.Info("BRIDGE", "Ending automode")

			return
		}

		// Refresh Discord users from session state on each tick
		// This handles the case where GuildCreate fires after initial population
		b.refreshDiscordVoiceUsers()

		// Use consistent lock ordering to prevent deadlock: BridgeMutex -> MumbleUsersMutex -> DiscordUsersMutex
		b.BridgeMutex.Lock()
		b.MumbleUsersMutex.Lock()
		b.DiscordUsersMutex.Lock()

		// Check if bridge should be started
		if !b.Connected && b.MumbleUserCount > 0 && len(b.DiscordUsers) > 0 {
			b.Logger.Info("BRIDGE", "Users detected in mumble and discord, starting bridge")
			go b.StartBridge()
		}

		// Stop bridge when either side has no users (symmetric with start condition)
		if b.Connected && (b.MumbleUserCount == 0 || len(b.DiscordUsers) == 0) {
			b.Logger.Info("BRIDGE", "No users detected on one side, stopping bridge")
			go b.StopBridge() // Use graceful stop instead of direct signal
		}

		// Auto mode check completed - no periodic status logging

		b.DiscordUsersMutex.Unlock()
		b.MumbleUsersMutex.Unlock()
		b.BridgeMutex.Unlock()
	}
}

// This function sends messages based on the bridge configuration
func (b *BridgeState) discordSendMessage(msg string) {
	switch b.BridgeConfig.DiscordTextMode {
	case "disabled":
		b.Logger.Debug("MUMBLE→DISCORD", "Message not sent - Discord text mode is disabled")

		return
	case "channel":
		b.Logger.Debug("MUMBLE→DISCORD", fmt.Sprintf("Sending message to Discord channel: %s", b.DiscordChannelID))
		_, err := b.DiscordSession.ChannelMessageSend(b.DiscordChannelID, msg)
		if err != nil {
			b.Logger.Error("MUMBLE→DISCORD", fmt.Sprintf("Error sending message to Discord: %v", err))
		} else {
			b.Logger.Debug("MUMBLE→DISCORD", "Successfully sent message to Discord channel")
		}

		return
	case "user":
		b.Logger.Debug("MUMBLE→DISCORD", fmt.Sprintf("Sending direct messages to %d Discord users", len(b.DiscordUsers)))
		b.DiscordUsersMutex.Lock()
		defer b.DiscordUsersMutex.Unlock()

		for id := range b.DiscordUsers {
			du := b.DiscordUsers[id]
			if du.dm != nil {
				b.Logger.Debug("MUMBLE→DISCORD", fmt.Sprintf("Sending DM to user: %s", du.username))
				_, err := b.DiscordSession.ChannelMessageSend(du.dm.ID, msg)
				if err != nil {
					b.Logger.Error("MUMBLE→DISCORD", fmt.Sprintf("Error sending DM to user %s: %v", du.username, err))
				} else {
					b.Logger.Debug("MUMBLE→DISCORD", fmt.Sprintf("Successfully sent DM to user: %s", du.username))
				}
			} else {
				b.Logger.Debug("MUMBLE→DISCORD", fmt.Sprintf("No DM channel available for user: %s", du.username))
			}
		}

		return
	default:
		b.Logger.Warn("MUMBLE→DISCORD", "Invalid DiscordTextMode")

		return
	}
}
