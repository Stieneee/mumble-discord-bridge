package bridgelib

import (
	"errors"
	"log"
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

	// The lock for protecting access to the bridge
	mu sync.Mutex

	// Channel to signal bridge to stop
	stopChan chan bool
	
	// Flag to track if the bridge has been stopped
	stopped bool
}

// NewBridgeInstance creates a new bridge instance
func NewBridgeInstance(id string, config *BridgeConfig, discordProvider DiscordProvider) (*BridgeInstance, error) {
	// Create the bridge instance
	inst := &BridgeInstance{
		ID:              id,
		config:          config,
		discordProvider: discordProvider,
		stopChan:        make(chan bool),
	}

	// Create the internal bridge state
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
	}

	// Setup Mumble config
	inst.State.BridgeConfig.MumbleConfig = gumble.NewConfig()
	inst.State.BridgeConfig.MumbleConfig.Username = config.MumbleUsername
	inst.State.BridgeConfig.MumbleConfig.Password = config.MumblePassword
	inst.State.BridgeConfig.MumbleConfig.AudioInterval = time.Millisecond * 10

	// Create the Mumble listener
	inst.State.MumbleListener = &bridge.MumbleListener{
		Bridge: inst.State,
	}

	// Attach the Mumble listener
	inst.setupMumbleListeners()

	// Set the Discord session from the provider
	inst.State.DiscordSession = inst.discordProvider.GetSession()

	// Create the Discord listener
	inst.State.DiscordListener = &bridge.DiscordListener{
		Bridge: inst.State,
	}
	
	// Register Discord event handlers
	log.Printf("[BRIDGELIB] Registering Discord event handlers for bridge: %s", id)
	
	// Register with the Discord session
	inst.State.DiscordSession.AddHandler(inst.State.DiscordListener.MessageCreate)
	inst.State.DiscordSession.AddHandler(inst.State.DiscordListener.GuildCreate)
	inst.State.DiscordSession.AddHandler(inst.State.DiscordListener.VoiceUpdate)
	
	// Also register with the shared client's routing system
	if sharedClient, ok := inst.discordProvider.(*SharedDiscordClient); ok {
		log.Printf("[BRIDGELIB] Registering with SharedDiscordClient's message router for GID: %s, CID: %s", 
			config.DiscordGID, config.DiscordCID)
		
		sharedClient.RegisterMessageHandler(
			config.DiscordGID, 
			config.DiscordCID, 
			inst.State.DiscordListener.MessageCreate)
			
		log.Printf("[BRIDGELIB] Message handler registered successfully")
	} else {
		log.Printf("[BRIDGELIB] Not using SharedDiscordClient, message routing will use global handlers only")
	}

	// Set the bridge mode
	inst.setBridgeMode(config.Mode)

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

	// Reset the stopped flag and create new stopChan if previously stopped
	if b.stopped {
		b.stopped = false
		b.stopChan = make(chan bool)
		log.Printf("[BRIDGELIB] Resetting stopped bridge for restart: %s", b.ID)
	}

	log.Printf("[BRIDGELIB] Starting bridge: %s (Discord GID: %s, CID: %s)", 
		b.ID, b.config.DiscordGID, b.config.DiscordCID)
	
	// Log important configuration values
	log.Printf("[BRIDGELIB] Bridge configuration: ChatBridge=%v, MumbleDisableText=%v, DiscordTextMode=%s",
		b.config.ChatBridge, b.config.MumbleDisableText, b.config.DiscordTextMode)
		
	// Set the Discord channel ID
	b.State.DiscordChannelID = b.config.DiscordCID
	
	// Validate essential configuration
	if b.State.DiscordSession == nil {
		return errors.New("discord session is nil")
	}
	
	// Ensure Discord event handlers are properly registered
	log.Printf("[BRIDGELIB] Verifying Discord event handlers for bridge: %s", b.ID)
	
	// Re-register with the shared client's routing system to ensure handlers are registered
	if sharedClient, ok := b.discordProvider.(*SharedDiscordClient); ok {
		log.Printf("[BRIDGELIB] Re-registering with SharedDiscordClient's message router for GID: %s, CID: %s", 
			b.config.DiscordGID, b.config.DiscordCID)
		
		// First unregister any existing handlers to avoid duplicates
		// Then register the handler
		key := b.config.DiscordGID + ":" + b.config.DiscordCID
		sharedClient.messageHandlerMutex.Lock()
		delete(sharedClient.messageHandlers, key)
		sharedClient.messageHandlerMutex.Unlock()
		
		// Register the handler again
		sharedClient.RegisterMessageHandler(
			b.config.DiscordGID, 
			b.config.DiscordCID, 
			b.State.DiscordListener.MessageCreate)
			
		log.Printf("[BRIDGELIB] Message handler re-registered successfully")
	} else {
		log.Printf("[BRIDGELIB] WARNING: Not using SharedDiscordClient, message routing will use global handlers only")
	}

	// Start the bridge based on its mode
	log.Printf("[BRIDGELIB] Starting bridge in mode: %s", b.State.Mode)
	
	switch b.State.Mode {
	case bridge.BridgeModeAuto:
		log.Printf("[BRIDGELIB] Using Auto mode")
		b.State.AutoChanDie = make(chan bool)
		go b.State.AutoBridge()
	case bridge.BridgeModeConstant:
		log.Printf("[BRIDGELIB] Using Constant mode")
		go func() {
			for {
				select {
				case <-b.stopChan:
					log.Printf("[BRIDGELIB] Received stop signal, exiting constant mode loop")
					return
				default:
					log.Printf("[BRIDGELIB] Starting bridge in constant mode")
					
					// Start the bridge and verify it connected successfully
					b.State.StartBridge()
					
					// Check if the bridge is connected
					b.State.BridgeMutex.Lock()
					connected := b.State.Connected
					b.State.BridgeMutex.Unlock()
					
					if connected {
						log.Printf("[BRIDGELIB] Bridge successfully connected in constant mode")
					} else {
						log.Printf("[BRIDGELIB] WARNING: Bridge failed to connect in constant mode")
					}
					
					log.Printf("[BRIDGELIB] Bridge exited, waiting 5 seconds before restarting")
					time.Sleep(5 * time.Second)
				}
			}
		}()
	case bridge.BridgeModeManual:
		log.Printf("[BRIDGELIB] Using Manual mode")
		go func() {
			log.Printf("[BRIDGELIB] Starting bridge in manual mode")
			b.State.StartBridge()
			
			// Check if the bridge is connected
			b.State.BridgeMutex.Lock()
			connected := b.State.Connected
			b.State.BridgeMutex.Unlock()
			
			if connected {
				log.Printf("[BRIDGELIB] Bridge successfully connected in manual mode")
			} else {
				log.Printf("[BRIDGELIB] WARNING: Bridge failed to connect in manual mode")
			}
			
			log.Printf("[BRIDGELIB] Bridge in manual mode exited")
		}()
	}

	return nil
}

// Stop stops the bridge instance
func (b *BridgeInstance) Stop() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	// Check if already stopped
	if b.stopped {
		log.Printf("[BRIDGELIB] Bridge %s is already stopped", b.ID)
		return nil
	}

	log.Printf("[BRIDGELIB] Stopping bridge: %s", b.ID)
	
	// Signal to the bridge to stop
	close(b.stopChan)
	b.stopped = true

	// Stop the auto bridge if it's running
	if b.State.Mode == bridge.BridgeModeAuto && b.State.AutoChanDie != nil {
		b.State.AutoChanDie <- true
	}

	// Stop the bridge if it's connected
	b.State.BridgeMutex.Lock()
	if b.State.Connected {
		b.State.BridgeDie <- true
		b.State.WaitExit.Wait()
	}
	b.State.BridgeMutex.Unlock()

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
