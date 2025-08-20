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
)

// DiscordProvider is an interface for providing Discord functionality
type DiscordProvider interface {
	// RegisterHandler registers a handler for Discord events
	RegisterHandler(handlerFunc interface{})

	// JoinVoiceChannel joins a voice channel
	JoinVoiceChannel(guildID, channelID string) (*discordgo.VoiceConnection, error)

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
	
	// Logger for the bridge instance
	Logger Logger
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
	logger Logger

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
	logger := config.Logger
	if logger == nil {
		logger = NewConsoleLogger()
	}
	
	// Create bridge-specific logger with the bridge ID
	bridgeLogger := logger.WithBridgeID(id)
	
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
					b.logger.Info("BRIDGE_STOP", "Context cancelled, exiting constant mode loop")
					return
				default:
					b.logger.Info("BRIDGE_MODE", "Starting bridge in constant mode")
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
						b.logger.Info("BRIDGE_STOP", "Context cancelled during wait, exiting constant mode loop")
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
	b.logger.Debug("BRIDGE_STOP", "Cancelling context")
	
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

	b.logger.Info("BRIDGE_STOP", "Bridge stopped successfully")
	return nil
}

// GetStatus returns the status of the bridge
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
	if b.State.Connected && !b.State.StartTime.IsZero() {
		uptime = time.Since(b.State.StartTime).Milliseconds()
	}

	return map[string]interface{}{
		"id":            b.ID,
		"connected":     b.State.Connected,
		"mode":          b.State.Mode.String(),
		"mumbleUsers":   len(b.State.MumbleUsers),
		"discordUsers":  len(b.State.DiscordUsers),
		"uptime":        uptime,
		"mumbleAddress": b.config.MumbleAddress,
		"mumblePort":    b.config.MumblePort,
		"discordGID":    b.config.DiscordGID,
		"discordCID":    b.config.DiscordCID,
	}
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

// Helper function to split the channel string into a slice of strings
func splitChannel(channel string) []string {
	if channel == "" {
		return []string{}
	}

	// Split the channel string by "/"
	// Example: "Root/Games/Minecraft" => ["Root", "Games", "Minecraft"]
	return strings.Split(channel, "/")
}
