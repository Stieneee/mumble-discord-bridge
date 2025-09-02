// Package bridgelib provides a high-level bridge instance management API.
package bridgelib

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/stieneee/gumble/gumble"
	"github.com/stieneee/gumble/gumbleutil"
	"github.com/stieneee/mumble-discord-bridge/internal/bridge"
	"github.com/stieneee/mumble-discord-bridge/pkg/logger"
)

const (
	// Service names
	serviceDiscord = "discord"
	serviceMumble  = "mumble"
)

// DiscordProvider is an interface for providing Discord functionality
type DiscordProvider interface {
	// RegisterHandler registers a handler for Discord events
	RegisterHandler(handlerFunc interface{})

	// SendMessage sends a message to a channel
	SendMessage(channelID, content string) (*discordgo.Message, error)

	// GetSession returns the underlying Discord session
	GetSession() *discordgo.Session
}

// BridgeConfig holds the configuration for a bridge instance
type BridgeConfig struct {
	// The command prefix for the bot
	Command string

	// Mumble Configuration
	MumbleAddress     string
	MumblePort        int
	MumbleUsername    string
	MumblePassword    string
	MumbleInsecure    bool
	MumbleCertificate string
	MumbleChannel     string
	MumbleSendBuffer  int
	MumbleDisableText bool
	MumbleCommand     bool
	MumbleBotFlag     bool

	// Discord Configuration
	DiscordGID              string
	DiscordCID              string
	DiscordSendBuffer       int
	DiscordTextMode         string
	DiscordDisableBotStatus bool
	DiscordCommand          bool

	// Bridge Configuration
	ChatBridge bool
	Mode       string
	Version    string

	// Event configuration
	EventBufferSize int

	// Logger for the bridge instance
	Logger logger.Logger
}

// BridgeInstance represents a single bridge instance
type BridgeInstance struct {
	// The internal bridge state
	State *bridge.BridgeState

	// The Discord provider
	discordProvider DiscordProvider

	// Bridge configuration
	config *BridgeConfig

	// Instance ID
	ID string

	// Logger for this bridge instance
	logger logger.Logger

	// Event handling
	eventDispatcher *EventDispatcher

	// The lock for protecting access to the bridge
	mu sync.Mutex

	// Flag to track if the bridge has been stopped
	stopped bool

	// Context for graceful shutdown
	ctx    context.Context
	cancel context.CancelFunc
}

// NewBridgeInstance creates a new bridge instance
func NewBridgeInstance(id string, config *BridgeConfig, discordProvider DiscordProvider) (*BridgeInstance, error) {
	return NewBridgeInstanceWithContext(context.Background(), id, config, discordProvider)
}

// NewBridgeInstanceWithContext creates a new bridge instance with context
func NewBridgeInstanceWithContext(ctx context.Context, id string, config *BridgeConfig, discordProvider DiscordProvider) (*BridgeInstance, error) {
	// Use provided logger or create a default console logger
	lgr := config.Logger
	if lgr == nil {
		lgr = logger.NewConsoleLogger()
	}

	// Create bridge-specific logger with the bridge ID
	bridgeLogger := lgr.WithBridgeID(id)

	bridgeLogger.Debug("BRIDGE_INIT", "Starting bridge instance creation")
	bridgeLogger.Debug("BRIDGE_INIT", fmt.Sprintf("Configuration: Mumble=%s:%d, Discord=%s:%s, Mode=%s",
		config.MumbleAddress, config.MumblePort, config.DiscordGID, config.DiscordCID, config.Mode))

	// Create context for this bridge instance
	bridgeCtx, cancel := context.WithCancel(ctx)

	// Create the bridge instance
	inst := &BridgeInstance{
		ID:              id,
		config:          config,
		discordProvider: discordProvider,
		logger:          bridgeLogger,
		ctx:             bridgeCtx,
		cancel:          cancel,
	}

	bridgeLogger.Debug("BRIDGE_INIT", "Bridge instance struct created successfully")

	// Initialize event dispatcher
	bufferSize := config.EventBufferSize
	if bufferSize <= 0 {
		bufferSize = 100 // Default buffer size
	}
	inst.eventDispatcher = NewEventDispatcher(id, bufferSize)
	inst.eventDispatcher.Start()
	bridgeLogger.Debug("BRIDGE_INIT", "Event dispatcher created and started")

	// Create the internal bridge state
	bridgeLogger.Debug("BRIDGE_INIT", "Creating internal bridge state")
	inst.State = &bridge.BridgeState{
		BridgeConfig: &bridge.BridgeConfig{
			Command:                    config.Command,
			MumbleAddr:                 config.MumbleAddress + ":" + strconv.Itoa(config.MumblePort),
			MumbleInsecure:             config.MumbleInsecure,
			MumbleCertificate:          config.MumbleCertificate,
			MumbleChannel:              splitChannel(config.MumbleChannel),
			MumbleStartStreamCount:     config.MumbleSendBuffer / 10,
			MumbleDisableText:          config.MumbleDisableText,
			MumbleCommand:              config.MumbleCommand,
			MumbleBotFlag:              config.MumbleBotFlag,
			GID:                        config.DiscordGID,
			CID:                        config.DiscordCID,
			DiscordStartStreamingCount: config.DiscordSendBuffer / 10,
			DiscordTextMode:            config.DiscordTextMode,
			DiscordDisableBotStatus:    config.DiscordDisableBotStatus,
			DiscordCommand:             config.DiscordCommand,
			ChatBridge:                 config.ChatBridge,
			Version:                    config.Version,
		},
		Connected:    false,
		DiscordUsers: make(map[string]bridge.DiscordUser),
		MumbleUsers:  make(map[string]bool),
		Logger:       bridgeLogger,
	}

	// Set back-reference to enable event forwarding
	inst.State.BridgeInstance = inst

	// Setup Mumble config
	bridgeLogger.Debug("BRIDGE_INIT", "Setting up Mumble configuration")
	inst.State.BridgeConfig.MumbleConfig = gumble.NewConfig()
	inst.State.BridgeConfig.MumbleConfig.Username = config.MumbleUsername
	inst.State.BridgeConfig.MumbleConfig.Password = config.MumblePassword
	inst.State.BridgeConfig.MumbleConfig.AudioInterval = time.Millisecond * 10
	bridgeLogger.Debug("BRIDGE_INIT", fmt.Sprintf("Mumble config created for user: %s", config.MumbleUsername))

	// Create the Mumble listener
	bridgeLogger.Debug("BRIDGE_INIT", "Creating Mumble listener")
	inst.State.MumbleListener = &bridge.MumbleListener{
		Bridge: inst.State,
	}

	// Attach the Mumble listener
	bridgeLogger.Debug("BRIDGE_INIT", "Setting up Mumble event handlers")
	inst.setupMumbleListeners()
	bridgeLogger.Debug("BRIDGE_INIT", "Mumble listeners attached successfully")

	// Set the Discord session from the provider
	bridgeLogger.Debug("BRIDGE_INIT", "Getting Discord session from provider")
	inst.State.DiscordSession = inst.discordProvider.GetSession()
	bridgeLogger.Debug("BRIDGE_INIT", "Discord session obtained successfully")

	// Create the Discord listener
	bridgeLogger.Debug("BRIDGE_INIT", "Creating Discord listener")
	inst.State.DiscordListener = &bridge.DiscordListener{
		Bridge: inst.State,
	}
	bridgeLogger.Debug("BRIDGE_INIT", "Discord listener created successfully")

	// Register Discord event handlers
	bridgeLogger.Info("BRIDGE_SETUP", "Registering Discord event handlers")

	// Register with the Discord session
	inst.State.DiscordSession.AddHandler(inst.State.DiscordListener.MessageCreate)
	inst.State.DiscordSession.AddHandler(inst.State.DiscordListener.GuildCreate)
	inst.State.DiscordSession.AddHandler(inst.State.DiscordListener.VoiceUpdate)

	// Also register with the shared client's routing system
	if sharedClient, ok := inst.discordProvider.(*SharedDiscordClient); ok {
		bridgeLogger.Info("BRIDGE_SETUP", fmt.Sprintf("Registering with SharedDiscordClient's message router for GID: %s, CID: %s", config.DiscordGID, config.DiscordCID))

		sharedClient.RegisterMessageHandler(
			config.DiscordGID,
			config.DiscordCID,
			inst.State.DiscordListener.MessageCreate)

		bridgeLogger.Info("BRIDGE_SETUP", "Message handler registered successfully")
	} else {
		bridgeLogger.Info("BRIDGE_SETUP", "Not using SharedDiscordClient, message routing will use global handlers only")
	}

	// Set the bridge mode
	bridgeLogger.Debug("BRIDGE_INIT", fmt.Sprintf("Setting bridge mode to: %s", config.Mode))
	inst.setBridgeMode(config.Mode)
	bridgeLogger.Debug("BRIDGE_INIT", fmt.Sprintf("Bridge mode set to: %s", inst.State.Mode.String()))

	bridgeLogger.Info("BRIDGE_INIT", "Bridge instance created successfully")

	return inst, nil
}

// setupMumbleListeners sets up the Mumble event listeners
func (b *BridgeInstance) setupMumbleListeners() {
	// Create a listener with all the event handlers
	b.State.BridgeConfig.MumbleConfig.Attach(gumbleutil.Listener{
		Connect:     b.State.MumbleListener.MumbleConnect,
		UserChange:  b.State.MumbleListener.MumbleUserChange,
		TextMessage: b.State.MumbleListener.MumbleTextMessage,
	})
}

// Start starts the bridge instance
func (b *BridgeInstance) Start() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	// Reset the stopped flag if previously stopped
	b.logger.Debug("BRIDGE_START", fmt.Sprintf("Bridge stopped state: %v", b.stopped))
	if b.stopped {
		b.stopped = false
		b.logger.Info("BRIDGE_START", "Resetting stopped bridge for restart")
	}

	b.logger.Info("BRIDGE_START", fmt.Sprintf("Starting bridge (Discord GID: %s, CID: %s)", b.config.DiscordGID, b.config.DiscordCID))

	// Log important configuration values
	b.logger.Info("BRIDGE_CONFIG", fmt.Sprintf("ChatBridge=%v, MumbleDisableText=%v, DiscordTextMode=%s", b.config.ChatBridge, b.config.MumbleDisableText, b.config.DiscordTextMode))

	// Emit bridge starting event
	if b.eventDispatcher != nil {
		b.eventDispatcher.EmitEvent(EventBridgeStarting, map[string]interface{}{
			"discord_gid":    b.config.DiscordGID,
			"discord_cid":    b.config.DiscordCID,
			"mumble_address": b.config.MumbleAddress,
			"mumble_port":    b.config.MumblePort,
			"mode":           b.config.Mode,
		}, nil)
	}

	// Set the Discord channel ID
	b.logger.Debug("BRIDGE_START", fmt.Sprintf("Setting Discord channel ID: %s", b.config.DiscordCID))
	b.State.DiscordChannelID = b.config.DiscordCID

	// Validate essential configuration
	b.logger.Debug("BRIDGE_START", "Validating Discord session")
	if b.State.DiscordSession == nil {
		b.logger.Error("BRIDGE_START", "Discord session is nil - cannot start bridge")

		return errors.New("discord session is nil")
	}
	b.logger.Debug("BRIDGE_START", "Discord session validation passed")

	// Discord event handlers were already registered during bridge creation
	b.logger.Debug("BRIDGE_VERIFY", "Discord event handlers already registered during bridge creation")

	// Start the bridge based on its mode
	b.logger.Info("BRIDGE_MODE", fmt.Sprintf("Starting bridge in mode: %s", b.State.Mode))

	switch b.State.Mode {
	case bridge.BridgeModeAuto:
		b.logger.Info("BRIDGE_MODE", "Using Auto mode")
		b.logger.Debug("BRIDGE_MODE", "Creating auto channel die signal")
		b.State.AutoChanDie = make(chan bool)
		b.logger.Debug("BRIDGE_MODE", "Starting auto bridge goroutine")
		go b.State.AutoBridge()
		b.logger.Debug("BRIDGE_MODE", "Auto bridge goroutine started")
	case bridge.BridgeModeConstant:
		b.logger.Info("BRIDGE_MODE", "Using Constant mode")
		b.logger.Debug("BRIDGE_MODE", "Starting constant mode goroutine")
		go func() {
			for {
				select {
				case <-b.ctx.Done():
					b.logger.Info("BRIDGE_STOP", "Context canceled, exiting constant mode loop")

					return
				default:
					b.logger.Info("BRIDGE_MODE", "Starting bridge in constant mode")
					
					// Set DiscordChannelID from config for constant mode
					b.State.DiscordChannelID = b.config.DiscordCID
					b.logger.Debug("BRIDGE_MODE", fmt.Sprintf("Set DiscordChannelID to %s", b.config.DiscordCID))
					
					b.logger.Debug("BRIDGE_START", "Calling State.StartBridge()")

					// Start the bridge and verify it connected successfully
					b.State.StartBridge()
					b.logger.Debug("BRIDGE_START", "State.StartBridge() returned")

					// Check if the bridge is connected
					b.State.BridgeMutex.Lock()
					connected := b.State.Connected
					b.State.BridgeMutex.Unlock()

					if connected {
						b.logger.Info("BRIDGE_STATUS", "Bridge successfully connected in constant mode")
					} else {
						b.logger.Warn("BRIDGE_STATUS", "Bridge failed to connect in constant mode")
					}

					b.logger.Info("BRIDGE_STATUS", "Bridge exited, waiting 5 seconds before restarting")

					// Wait 5 seconds, but check for context cancellation
					select {
					case <-b.ctx.Done():
						b.logger.Info("BRIDGE_STOP", "Context canceled during wait, exiting constant mode loop")

						return
					case <-time.After(5 * time.Second):
						// Continue to next iteration
					}
				}
			}
		}()
	case bridge.BridgeModeManual:
		b.logger.Info("BRIDGE_MODE", "Using Manual mode")
		b.logger.Debug("BRIDGE_MODE", "Starting manual mode goroutine")
		go func() {
			b.logger.Info("BRIDGE_MODE", "Starting bridge in manual mode")
			
			// Set DiscordChannelID from config for manual mode
			b.State.DiscordChannelID = b.config.DiscordCID
			b.logger.Debug("BRIDGE_MODE", fmt.Sprintf("Set DiscordChannelID to %s", b.config.DiscordCID))
			
			b.logger.Debug("BRIDGE_START", "Calling State.StartBridge() in manual mode")
			b.State.StartBridge()
			b.logger.Debug("BRIDGE_START", "State.StartBridge() returned in manual mode")

			// Check if the bridge is connected
			b.State.BridgeMutex.Lock()
			connected := b.State.Connected
			b.State.BridgeMutex.Unlock()

			if connected {
				b.logger.Info("BRIDGE_STATUS", "Bridge successfully connected in manual mode")
			} else {
				b.logger.Warn("BRIDGE_STATUS", "Bridge failed to connect in manual mode")
			}

			b.logger.Info("BRIDGE_STATUS", "Bridge in manual mode exited")
		}()
	}

	// Emit bridge started event
	if b.eventDispatcher != nil {
		b.eventDispatcher.EmitEvent(EventBridgeStarted, map[string]interface{}{
			"mode": b.config.Mode,
		}, nil)
	}

	return nil
}

// Stop stops the bridge instance
func (b *BridgeInstance) Stop() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.logger.Debug("BRIDGE_STOP", "Stop method called")

	// Check if already stopped
	if b.stopped {
		b.logger.Info("BRIDGE_STOP", "Bridge is already stopped")

		return nil
	}

	b.logger.Info("BRIDGE_STOP", "Stopping bridge")

	// Emit bridge stopping event
	if b.eventDispatcher != nil {
		b.eventDispatcher.EmitEventSync(EventBridgeStopping, map[string]interface{}{}, nil)
	}

	b.logger.Debug("BRIDGE_STOP", "Canceling context")

	// Cancel the context to signal all operations to stop
	b.cancel()

	// Mark bridge as stopped
	b.stopped = true
	b.logger.Debug("BRIDGE_STOP", "Bridge marked as stopped")

	// Stop the auto bridge if it's running
	if b.State.Mode == bridge.BridgeModeAuto && b.State.AutoChanDie != nil {
		b.logger.Debug("BRIDGE_STOP", "Sending stop signal to auto bridge")
		b.State.AutoChanDie <- true
		b.logger.Debug("BRIDGE_STOP", "Auto bridge stop signal sent")
	}

	// Stop the bridge if it's connected
	b.logger.Debug("BRIDGE_STOP", "Checking bridge connection status")
	b.State.BridgeMutex.Lock()
	connected := b.State.Connected
	if connected {
		b.logger.Debug("BRIDGE_STOP", "Bridge is connected, sending die signal")
		b.State.BridgeDie <- true
		b.logger.Debug("BRIDGE_STOP", "Waiting for bridge exit")
		b.State.WaitExit.Wait()
		b.logger.Debug("BRIDGE_STOP", "Bridge exit completed")
	} else {
		b.logger.Debug("BRIDGE_STOP", "Bridge is not connected, skipping die signal")
	}
	b.State.BridgeMutex.Unlock()

	// Clean up message handlers from SharedDiscordClient
	if sharedClient, ok := b.discordProvider.(*SharedDiscordClient); ok {
		b.logger.Debug("BRIDGE_STOP", fmt.Sprintf("Unregistering message handler for GID: %s, CID: %s", b.config.DiscordGID, b.config.DiscordCID))
		sharedClient.UnregisterMessageHandler(
			b.config.DiscordGID,
			b.config.DiscordCID,
			b.State.DiscordListener.MessageCreate)
		b.logger.Debug("BRIDGE_STOP", "Message handler unregistered successfully")
	}

	// Emit bridge stopped event and stop event dispatcher
	if b.eventDispatcher != nil {
		b.eventDispatcher.EmitEventSync(EventBridgeStopped, map[string]interface{}{}, nil)
		b.eventDispatcher.Stop()
		b.logger.Debug("BRIDGE_STOP", "Event dispatcher stopped")
	}

	b.logger.Info("BRIDGE_STOP", "Bridge stopped successfully")

	return nil
}

// BridgeStatus represents comprehensive bridge status information
type BridgeStatus struct {
	ID               string                 `json:"id"`
	State            string                 `json:"state"`
	DiscordConnected bool                   `json:"discord_connected"`
	MumbleConnected  bool                   `json:"mumble_connected"`
	DiscordUsers     int                    `json:"discord_users"`
	MumbleUsers      int                    `json:"mumble_users"`
	Uptime           time.Duration          `json:"uptime"`
	Config           map[string]interface{} `json:"config"`
}

// GetStatus returns the basic status of the bridge (legacy method)
func (b *BridgeInstance) GetStatus() map[string]interface{} {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.State.BridgeMutex.Lock()
	defer b.State.BridgeMutex.Unlock()

	b.State.MumbleUsersMutex.Lock()
	defer b.State.MumbleUsersMutex.Unlock()

	b.State.DiscordUsersMutex.Lock()
	defer b.State.DiscordUsersMutex.Unlock()

	uptime := int64(0)
	var connected bool

	b.State.BridgeMutex.Lock()
	connected = b.State.Connected
	startTime := b.State.StartTime
	b.State.BridgeMutex.Unlock()

	if connected && !startTime.IsZero() {
		uptime = time.Since(startTime).Milliseconds()
	}

	return map[string]interface{}{
		"id":            b.ID,
		"connected":     connected,
		"mode":          b.State.Mode.String(),
		"mumbleUsers":   len(b.State.MumbleUsers),
		"discordUsers":  len(b.State.DiscordUsers),
		"uptime":        uptime,
		"mumbleAddress": b.config.MumbleAddress,
		"mumblePort":    b.config.MumblePort,
		"discordGID":    b.config.DiscordGID,
		"discordCID":    b.config.DiscordCID,
		"stopped":       b.stopped,
	}
}

// GetDetailedStatus returns comprehensive bridge status information
func (b *BridgeInstance) GetDetailedStatus() BridgeStatus {
	b.mu.Lock()
	defer b.mu.Unlock()

	status := BridgeStatus{
		ID:     b.ID,
		Config: make(map[string]interface{}),
	}

	// Get basic state
	if b.stopped {
		status.State = "stopped"
	} else {
		b.State.BridgeMutex.Lock()
		connected := b.State.Connected
		b.State.BridgeMutex.Unlock()

		if connected {
			status.State = "running"
		} else {
			status.State = "starting"
		}
	}

	// Get user counts from bridge state
	if b.State != nil {
		b.State.MumbleUsersMutex.Lock()
		b.State.DiscordUsersMutex.Lock()
		status.MumbleUsers = len(b.State.MumbleUsers)
		status.DiscordUsers = len(b.State.DiscordUsers)
		b.State.DiscordUsersMutex.Unlock()
		b.State.MumbleUsersMutex.Unlock()

		// Calculate uptime
		b.State.BridgeMutex.Lock()
		connected := b.State.Connected
		startTime := b.State.StartTime
		b.State.BridgeMutex.Unlock()

		if connected && !startTime.IsZero() {
			status.Uptime = time.Since(startTime)
		}
	}

	// Add configuration information
	status.Config["mumble_address"] = b.config.MumbleAddress
	status.Config["mumble_port"] = b.config.MumblePort
	status.Config["discord_gid"] = b.config.DiscordGID
	status.Config["discord_cid"] = b.config.DiscordCID
	status.Config["mode"] = b.config.Mode

	return status
}

// setBridgeMode sets the bridge mode based on a string
func (b *BridgeInstance) setBridgeMode(mode string) {
	switch mode {
	case "auto":
		b.State.Mode = bridge.BridgeModeAuto
	case "manual":
		b.State.Mode = bridge.BridgeModeManual
	case "constant", "":
		// Default to constant mode if not specified
		b.State.Mode = bridge.BridgeModeConstant
	default:
		// For any unexpected value, use constant mode
		b.State.Mode = bridge.BridgeModeConstant
	}
}

// RegisterHandler registers a handler for a specific event type
func (b *BridgeInstance) RegisterHandler(eventType BridgeEventType, handler BridgeEventHandler) {
	if b.eventDispatcher != nil {
		b.eventDispatcher.RegisterHandler(eventType, handler)
	}
}

// EmitConnectionEvent emits a connection event (implements BridgeEventEmitter)
func (b *BridgeInstance) EmitConnectionEvent(service string, eventTypeInt int, connected bool, err error) {
	if b.eventDispatcher == nil {
		return
	}

	// Map service and event type to BridgeEventType
	var eventType BridgeEventType
	switch service {
	case serviceDiscord:
		switch eventTypeInt {
		case 0:
			eventType = EventDiscordConnecting
		case 1:
			eventType = EventDiscordConnected
		case 2:
			eventType = EventDiscordDisconnected
		case 3:
			eventType = EventDiscordReconnecting
		case 4:
			eventType = EventDiscordConnectionFailed
		default:
			return
		}
	case serviceMumble:
		switch eventTypeInt {
		case 0:
			eventType = EventMumbleConnecting
		case 1:
			eventType = EventMumbleConnected
		case 2:
			eventType = EventMumbleDisconnected
		case 3:
			eventType = EventMumbleReconnecting
		case 4:
			eventType = EventMumbleConnectionFailed
		default:
			return
		}
	default:
		return
	}

	// Emit event
	b.eventDispatcher.EmitEvent(eventType, map[string]interface{}{
		"service":   service,
		"connected": connected,
	}, err)
}

// EmitUserEvent emits a user join/leave event (implements BridgeInstance interface)
func (b *BridgeInstance) EmitUserEvent(service string, eventTypeInt int, username string, err error) {
	if b.eventDispatcher == nil {
		return
	}

	// Map service and event type to BridgeEventType
	var eventType BridgeEventType
	switch service {
	case serviceDiscord:
		switch eventTypeInt {
		case 0:
			eventType = EventUserJoinedDiscord
		case 1:
			eventType = EventUserLeftDiscord
		default:
			return
		}
	case serviceMumble:
		switch eventTypeInt {
		case 0:
			eventType = EventUserJoinedMumble
		case 1:
			eventType = EventUserLeftMumble
		default:
			return
		}
	default:
		return
	}

	// Emit event
	b.eventDispatcher.EmitEvent(eventType, map[string]interface{}{
		"service":  service,
		"username": username,
	}, err)
}

// Helper function to split the channel string into a slice of strings
func splitChannel(channel string) []string {
	if channel == "" {
		return []string{}
	}

	// Strip leading and trailing quotes (both single and double)
	channel = strings.Trim(channel, "'\"")

	// Split the channel string by "/"
	// Example: "Root/Games/Minecraft" => ["Root", "Games", "Minecraft"]
	parts := strings.Split(channel, "/")

	// Trim whitespace from each part
	for i, part := range parts {
		parts[i] = strings.TrimSpace(part)
	}

	return parts
}
