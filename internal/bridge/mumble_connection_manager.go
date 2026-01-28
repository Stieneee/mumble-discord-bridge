package bridge

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/stieneee/gumble/gumble"
	"github.com/stieneee/mumble-discord-bridge/pkg/logger"
)

// MumbleConnectionManager manages Mumble connections with automatic reconnection
type MumbleConnectionManager struct {
	*BaseConnectionManager
	client             *gumble.Client
	config             *gumble.Config
	address            string
	tlsConfig          *tls.Config
	clientMutex        sync.RWMutex
	configMutex        sync.RWMutex                 // Protects address, config, and tlsConfig
	disconnectCh       chan *gumble.DisconnectEvent // Channel to signal disconnection events
	disconnectMux      sync.Mutex                   // Protects disconnectCh and disconnectChClosed
	disconnectChClosed bool                         // Flag to track if disconnectCh is closed
}

// NewMumbleConnectionManager creates a new Mumble connection manager
func NewMumbleConnectionManager(address string, config *gumble.Config, tlsConfig *tls.Config, logger logger.Logger, eventEmitter BridgeEventEmitter) *MumbleConnectionManager {
	base := NewBaseConnectionManager(logger, "mumble", eventEmitter)

	manager := &MumbleConnectionManager{
		BaseConnectionManager: base,
		address:               address,
		config:                config,
		tlsConfig:             tlsConfig,
		disconnectCh:          make(chan *gumble.DisconnectEvent, 1),
	}

	// Attach this connection manager as an event listener to the gumble config
	if config != nil {
		config.Attach(manager)
	}

	return manager
}

// Start begins the Mumble connection process with automatic reconnection
func (m *MumbleConnectionManager) Start(ctx context.Context) error {
	m.logger.Info("MUMBLE_CONN", "Starting Mumble connection manager")

	// Initialize context for proper cancellation chain
	m.InitContext(ctx)

	// Start connection management goroutine
	go m.connectionLoop(m.ctx)

	return nil
}

// connectionLoop manages the connection lifecycle with reconnection logic
func (m *MumbleConnectionManager) connectionLoop(ctx context.Context) {
	defer m.disconnectInternal()

	for {
		// Check if we're in a permanent failure state (kicked/banned)
		if m.GetStatus() == ConnectionFailed {
			m.logger.Info("MUMBLE_CONN", "Connection in permanent failure state, stopping reconnection attempts")

			return
		}

		select {
		case <-ctx.Done():
			m.logger.Info("MUMBLE_CONN", "Connection loop canceled")

			return
		default:
		}

		// Attempt connection
		if err := m.connect(); err != nil {
			m.logger.Error("MUMBLE_CONN", fmt.Sprintf("Connection failed: %v", err))
			m.SetStatus(ConnectionReconnecting, err)

			// Wait before retrying
			select {
			case <-time.After(2 * time.Second):
				continue
			case <-ctx.Done():
				return
			}
		}

		// Connection successful - check if context is still active
		select {
		case <-ctx.Done():
			return
		default:
			m.SetStatus(ConnectionConnected, nil)
			m.logger.Info("MUMBLE_CONN", "Mumble connection established")
		}

		// Wait for disconnect event
		select {
		case <-ctx.Done():
			return
		case disconnectEvent := <-m.disconnectCh:
			m.handleDisconnectEvent(disconnectEvent)
			// Check if this was a permanent failure (kicked/banned)
			if m.GetStatus() == ConnectionFailed {
				m.logger.Info("MUMBLE_CONN", "Permanent failure, exiting connection loop")

				return
			}
		}
	}
}

// connect establishes a Mumble connection
func (m *MumbleConnectionManager) connect() error {
	m.SetStatus(ConnectionConnecting, nil)

	// Read configuration under lock
	m.configMutex.RLock()
	address := m.address
	config := m.config
	tlsConfig := m.tlsConfig
	m.configMutex.RUnlock()

	// Log connection attempt with redacted sensitive info
	configDebug := m.getRedactedConfigInfo()
	tlsDebug := m.getRedactedTLSInfo()
	m.logger.Debug("MUMBLE_CONN", fmt.Sprintf("Connecting to Mumble: Address=%s, Config=%+v, TLS=%+v",
		address, configDebug, tlsDebug))

	// Disconnect any existing connection
	m.disconnectInternal()

	// Attempt Mumble connection
	client, err := gumble.DialWithDialer(new(net.Dialer), address, config, tlsConfig)
	if err != nil {
		m.logger.Error("MUMBLE_CONN", fmt.Sprintf("Failed to dial Mumble server %s: %v", address, err))

		return fmt.Errorf("failed to connect to Mumble server: %w", err)
	}

	// Store connection
	m.clientMutex.Lock()
	m.client = client
	m.clientMutex.Unlock()

	m.logger.Debug("MUMBLE_CONN", fmt.Sprintf("Mumble connection established successfully to %s, client state: %d",
		address, client.State()))

	return nil
}

// disconnectInternal disconnects from Mumble without changing status
func (m *MumbleConnectionManager) disconnectInternal() {
	m.clientMutex.Lock()
	defer m.clientMutex.Unlock()

	if m.client != nil {
		m.logger.Debug("MUMBLE_CONN", "Disconnecting from Mumble")
		if err := m.client.Disconnect(); err != nil {
			m.logger.Error("MUMBLE_CONN", fmt.Sprintf("Error disconnecting from Mumble: %v", err))
		}
		m.client = nil
	}
}

// handleDisconnectEvent processes different types of disconnect events
func (m *MumbleConnectionManager) handleDisconnectEvent(event *gumble.DisconnectEvent) {
	switch event.Type {
	case gumble.DisconnectError:
		m.SetStatus(ConnectionReconnecting, fmt.Errorf("connection error: %s", event.String))
		m.logger.Warn("MUMBLE_CONN", fmt.Sprintf("Connection lost due to error: %s, attempting reconnection", event.String))
	case gumble.DisconnectKicked:
		m.SetStatus(ConnectionFailed, fmt.Errorf("kicked from server: %s", event.String))
		m.logger.Error("MUMBLE_CONN", fmt.Sprintf("Kicked from server: %s", event.String))
	case gumble.DisconnectBanned:
		m.SetStatus(ConnectionFailed, fmt.Errorf("banned from server: %s", event.String))
		m.logger.Error("MUMBLE_CONN", fmt.Sprintf("Banned from server: %s", event.String))
	case gumble.DisconnectUser:
		m.SetStatus(ConnectionReconnecting, nil)
		m.logger.Info("MUMBLE_CONN", "User-initiated disconnect, attempting reconnection")
	default:
		m.SetStatus(ConnectionReconnecting, fmt.Errorf("unknown disconnect: %s", event.String))
		m.logger.Warn("MUMBLE_CONN", fmt.Sprintf("Unknown disconnect type: %s, attempting reconnection", event.String))
	}
}

// Stop gracefully stops the Mumble connection manager
func (m *MumbleConnectionManager) Stop() error {
	m.logger.Info("MUMBLE_CONN", "Stopping Mumble connection manager")

	// Stop the base connection manager (cancels context)
	if err := m.BaseConnectionManager.Stop(); err != nil {
		m.logger.Error("MUMBLE_CONN", fmt.Sprintf("Error stopping base connection manager: %v", err))
	}

	// Disconnect from Mumble
	m.disconnectInternal()

	// Close the disconnect channel to prevent any further events
	m.disconnectMux.Lock()
	if !m.disconnectChClosed {
		m.disconnectChClosed = true
		close(m.disconnectCh)
	}
	m.disconnectMux.Unlock()

	return nil
}

// GetClient returns the current Mumble client (thread-safe)
func (m *MumbleConnectionManager) GetClient() *gumble.Client {
	m.clientMutex.RLock()
	defer m.clientMutex.RUnlock()

	return m.client
}

// EventListener implementation for gumble events
// We only care about Connect and Disconnect events for connection management

// OnConnect handles gumble connection events
func (m *MumbleConnectionManager) OnConnect(_ *gumble.ConnectEvent) {
	m.logger.Info("MUMBLE_CONN", "Connection event received")
	// Connection events are already handled by the connection loop
}

// OnDisconnect handles gumble disconnection events and signals the connection loop
func (m *MumbleConnectionManager) OnDisconnect(e *gumble.DisconnectEvent) {
	m.logger.Warn("MUMBLE_CONN", fmt.Sprintf("Disconnect event received: %s", e.String))

	// Signal the connection loop about the disconnection
	m.disconnectMux.Lock()
	defer m.disconnectMux.Unlock()

	// Check if channel is closed before attempting to send
	if m.disconnectChClosed {
		m.logger.Debug("MUMBLE_CONN", "Disconnect channel already closed, skipping event")

		return
	}

	select {
	case m.disconnectCh <- e:
		// Successfully sent disconnect signal
	default:
		// Channel is full, no need to send another event
		m.logger.Debug("MUMBLE_CONN", "Disconnect channel full, skipping event")
	}
}

// Required EventListener interface methods (unused for connection management)

// OnTextMessage implements gumble.EventListener interface (unused)
func (m *MumbleConnectionManager) OnTextMessage(_ *gumble.TextMessageEvent) {}

// OnUserChange implements gumble.EventListener interface (unused)
func (m *MumbleConnectionManager) OnUserChange(_ *gumble.UserChangeEvent) {}

// OnChannelChange implements gumble.EventListener interface (unused)
func (m *MumbleConnectionManager) OnChannelChange(_ *gumble.ChannelChangeEvent) {}

// OnPermissionDenied implements gumble.EventListener interface (unused)
func (m *MumbleConnectionManager) OnPermissionDenied(_ *gumble.PermissionDeniedEvent) {}

// OnUserList implements gumble.EventListener interface (unused)
func (m *MumbleConnectionManager) OnUserList(_ *gumble.UserListEvent) {}

// OnACL implements gumble.EventListener interface (unused)
func (m *MumbleConnectionManager) OnACL(_ *gumble.ACLEvent) {}

// OnBanList implements gumble.EventListener interface (unused)
func (m *MumbleConnectionManager) OnBanList(_ *gumble.BanListEvent) {}

// OnContextActionChange implements gumble.EventListener interface (unused)
func (m *MumbleConnectionManager) OnContextActionChange(_ *gumble.ContextActionChangeEvent) {}

// OnServerConfig implements gumble.EventListener interface (unused)
func (m *MumbleConnectionManager) OnServerConfig(_ *gumble.ServerConfigEvent) {}

// GetAudioOutgoing returns the audio outgoing channel from the Mumble client
func (m *MumbleConnectionManager) GetAudioOutgoing() chan<- gumble.AudioBuffer {
	m.clientMutex.RLock()
	defer m.clientMutex.RUnlock()

	if m.client == nil {
		return nil
	}

	return m.client.AudioOutgoing()
}

// GetSelfName safely returns the client's own name
func (m *MumbleConnectionManager) GetSelfName() string {
	m.clientMutex.RLock()
	client := m.client
	m.clientMutex.RUnlock()

	if client == nil {
		return ""
	}

	var name string
	client.Do(func() {
		if client.Self != nil {
			name = client.Self.Name
		}
	})

	return name
}

// GetChannelUsers safely returns the users in the client's current channel
func (m *MumbleConnectionManager) GetChannelUsers() []*gumble.User {
	m.clientMutex.RLock()
	client := m.client
	m.clientMutex.RUnlock()

	if client == nil {
		return []*gumble.User{}
	}

	var usersCopy []*gumble.User
	client.Do(func() {
		if client.Self != nil && client.Self.Channel != nil {
			// Create a proper copy of the users to avoid concurrent access issues
			for _, user := range client.Self.Channel.Users {
				if user != nil {
					usersCopy = append(usersCopy, user)
				}
			}
		}
	})

	return usersCopy
}

// Note: Audio listeners should be attached to the config before connection,
// not to the connection manager, to ensure they're active when client connects

// UpdateConfig updates the Mumble configuration (requires reconnection for most changes)
func (m *MumbleConnectionManager) UpdateConfig(newConfig *gumble.Config) error {
	m.logger.Info("MUMBLE_CONN", "Updating Mumble configuration")

	m.configMutex.Lock()
	m.config = newConfig
	m.configMutex.Unlock()

	// If currently connected, disconnect to trigger reconnection
	if m.IsConnected() {
		m.logger.Info("MUMBLE_CONN", "Disconnecting to apply config change")
		m.disconnectInternal()
	}

	return nil
}

// UpdateAddress updates the Mumble server address (requires reconnection)
func (m *MumbleConnectionManager) UpdateAddress(address string) error {
	m.configMutex.Lock()
	if m.address == address {
		m.configMutex.Unlock()

		return nil
	}

	m.logger.Info("MUMBLE_CONN", fmt.Sprintf("Changing address from %s to %s", m.address, address))
	m.address = address
	m.configMutex.Unlock()

	// If currently connected, disconnect to trigger reconnection
	if m.IsConnected() {
		m.logger.Info("MUMBLE_CONN", "Disconnecting to apply address change")
		m.disconnectInternal()
	}

	return nil
}

// GetAddress returns the Mumble server address
func (m *MumbleConnectionManager) GetAddress() string {
	m.configMutex.RLock()
	defer m.configMutex.RUnlock()

	return m.address
}

// GetConfig returns the Mumble configuration
func (m *MumbleConnectionManager) GetConfig() *gumble.Config {
	m.configMutex.RLock()
	defer m.configMutex.RUnlock()

	return m.config
}

// getRedactedConfigInfo returns config info with sensitive fields redacted for logging
func (m *MumbleConnectionManager) getRedactedConfigInfo() map[string]any {
	m.configMutex.RLock()
	config := m.config
	m.configMutex.RUnlock()

	if config == nil {
		return map[string]any{"config": "nil"}
	}

	return map[string]any{
		"Username":       config.Username,
		"Password":       fmt.Sprintf("[REDACTED - %d chars]", len(config.Password)),
		"Tokens":         fmt.Sprintf("[%d tokens]", len(config.Tokens)),
		"AudioInterval":  config.AudioInterval.String(),
		"AudioDataBytes": config.AudioDataBytes,
		"AudioFrameSize": config.AudioFrameSize(),
		"ClientType":     config.ClientType,
	}
}

// getRedactedTLSInfo returns TLS config info with sensitive fields redacted for logging
func (m *MumbleConnectionManager) getRedactedTLSInfo() map[string]any {
	m.configMutex.RLock()
	tlsConfig := m.tlsConfig
	m.configMutex.RUnlock()

	if tlsConfig == nil {
		return map[string]any{"tls": "nil"}
	}

	return map[string]any{
		"InsecureSkipVerify": tlsConfig.InsecureSkipVerify,
		"ServerName":         tlsConfig.ServerName,
		"MinVersion":         tlsConfig.MinVersion,
		"MaxVersion":         tlsConfig.MaxVersion,
		"CipherSuites":       "[REDACTED]",
		"Certificates":       fmt.Sprintf("[%d certificates]", len(tlsConfig.Certificates)),
		"RootCAs":            fmt.Sprintf("[%v]", tlsConfig.RootCAs != nil),
		"ClientCAs":          fmt.Sprintf("[%v]", tlsConfig.ClientCAs != nil),
	}
}
