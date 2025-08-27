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
	client      *gumble.Client
	config      *gumble.Config
	address     string
	tlsConfig   *tls.Config
	clientMutex sync.RWMutex
}

// NewMumbleConnectionManager creates a new Mumble connection manager
func NewMumbleConnectionManager(address string, config *gumble.Config, tlsConfig *tls.Config, logger logger.Logger, eventEmitter BridgeEventEmitter) *MumbleConnectionManager {
	base := NewBaseConnectionManager(logger, "mumble", eventEmitter)

	return &MumbleConnectionManager{
		BaseConnectionManager: base,
		address:               address,
		config:                config,
		tlsConfig:             tlsConfig,
	}
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
	for {
		select {
		case <-ctx.Done():
			m.logger.Info("MUMBLE_CONN", "Connection loop cancelled")
			m.disconnectInternal()
			return
		default:
		}

		// Attempt connection
		if err := m.connect(); err != nil {
			m.logger.Error("MUMBLE_CONN", fmt.Sprintf("Connection failed: %v", err))
			m.SetStatus(ConnectionReconnecting, err)

			// Wait before retrying
			select {
			case <-time.After(5 * time.Second):
				continue
			case <-ctx.Done():
				return
			}
		}

		// Connection successful
		m.SetStatus(ConnectionConnected, nil)
		m.logger.Info("MUMBLE_CONN", "Mumble connection established")

		// Wait for the connection to be lost (gumble library handles this)
		// We'll rely on the client's state to detect disconnections
		select {
		case <-ctx.Done():
			return
		case <-m.waitForConnectionLoss(ctx):
			// Connection lost, prepare for reconnection
			m.SetStatus(ConnectionReconnecting, nil)
			m.logger.Warn("MUMBLE_CONN", "Mumble connection lost, attempting reconnection")
		}
	}
}

// connect establishes a Mumble connection
func (m *MumbleConnectionManager) connect() error {
	m.SetStatus(ConnectionConnecting, nil)

	// Log connection attempt with redacted sensitive info
	configDebug := m.getRedactedConfigInfo()
	tlsDebug := m.getRedactedTLSInfo()
	m.logger.Debug("MUMBLE_CONN", fmt.Sprintf("Connecting to Mumble: Address=%s, Config=%+v, TLS=%+v",
		m.address, configDebug, tlsDebug))

	// Disconnect any existing connection
	m.disconnectInternal()

	// Attempt Mumble connection
	client, err := gumble.DialWithDialer(new(net.Dialer), m.address, m.config, m.tlsConfig)
	if err != nil {
		m.logger.Error("MUMBLE_CONN", fmt.Sprintf("Failed to dial Mumble server %s: %v", m.address, err))
		return fmt.Errorf("failed to connect to Mumble server: %w", err)
	}

	// Store connection
	m.clientMutex.Lock()
	m.client = client
	m.clientMutex.Unlock()

	m.logger.Debug("MUMBLE_CONN", fmt.Sprintf("Mumble connection established successfully to %s, client state: %d",
		m.address, client.State()))
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

// waitForConnectionLoss waits for the Mumble connection to be lost by monitoring the client state
func (m *MumbleConnectionManager) waitForConnectionLoss(ctx context.Context) <-chan bool {
	lostChan := make(chan bool, 1)

	go func() {
		defer func() {
			if r := recover(); r != nil {
				m.logger.Error("MUMBLE_CONN", fmt.Sprintf("Connection loss monitoring panic recovered: %v", r))
			}
			close(lostChan)
		}()

		// Check connection state periodically
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				// Add additional context check to prevent races
				if ctx.Err() != nil {
					return
				}

				m.clientMutex.RLock()
				client := m.client
				m.clientMutex.RUnlock()

				// If client is nil or not connected (states 1 or 2 are connected), consider it lost
				// StateConnected = 1 (syncing), StateSynced = 2 (fully ready)
				if client == nil || (client.State() != 1 && client.State() != 2) {
					select {
					case lostChan <- true:
					case <-ctx.Done():
						return
					default:
						// Channel is full or closed, connection already considered lost
					}
					return
				}
			}
		}
	}()

	return lostChan
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

	return nil
}

// GetClient returns the current Mumble client (thread-safe)
func (m *MumbleConnectionManager) GetClient() *gumble.Client {
	m.clientMutex.RLock()
	defer m.clientMutex.RUnlock()
	return m.client
}

// GetConnectionInfo returns Mumble-specific connection information
func (m *MumbleConnectionManager) GetConnectionInfo() map[string]interface{} {
	m.clientMutex.RLock()
	defer m.clientMutex.RUnlock()

	info := map[string]interface{}{
		"type":     "mumble",
		"address":  m.address,
		"username": m.config.Username,
		"state":    -1,
	}

	if m.client != nil {
		state := m.client.State()
		info["state"] = int(state)
		// Consider states 1 (StateConnected) and 2 (StateSynced) as connected
		info["connected"] = state == 1 || state == 2
	}

	return info
}

// GetAudioOutgoing returns the audio outgoing channel from the Mumble client
func (m *MumbleConnectionManager) GetAudioOutgoing() chan<- gumble.AudioBuffer {
	m.clientMutex.RLock()
	defer m.clientMutex.RUnlock()

	if m.client == nil {
		return nil
	}

	return m.client.AudioOutgoing()
}

// Note: Audio listeners should be attached to the config before connection,
// not to the connection manager, to ensure they're active when client connects

// UpdateConfig updates the Mumble configuration (requires reconnection for most changes)
func (m *MumbleConnectionManager) UpdateConfig(newConfig *gumble.Config) error {
	m.logger.Info("MUMBLE_CONN", "Updating Mumble configuration")

	// Store new config
	m.config = newConfig

	// If currently connected, trigger reconnection to apply new config
	if m.IsConnected() {
		m.logger.Info("MUMBLE_CONN", "Triggering reconnection for config change")
		go func() {
			m.disconnectInternal()
		}()
	}

	return nil
}

// UpdateAddress updates the Mumble server address (requires reconnection)
func (m *MumbleConnectionManager) UpdateAddress(address string) error {
	if m.address == address {
		return nil // No change needed
	}

	m.logger.Info("MUMBLE_CONN", fmt.Sprintf("Changing Mumble address from %s to %s", m.address, address))
	m.address = address

	// If currently connected, trigger reconnection to new address
	if m.IsConnected() {
		m.logger.Info("MUMBLE_CONN", "Triggering reconnection for address change")
		go func() {
			m.disconnectInternal()
		}()
	}

	return nil
}

// GetAddress returns the Mumble server address
func (m *MumbleConnectionManager) GetAddress() string {
	return m.address
}

// GetConfig returns the Mumble configuration
func (m *MumbleConnectionManager) GetConfig() *gumble.Config {
	return m.config
}

// getRedactedConfigInfo returns config info with sensitive fields redacted for logging
func (m *MumbleConnectionManager) getRedactedConfigInfo() map[string]interface{} {
	if m.config == nil {
		return map[string]interface{}{"config": "nil"}
	}

	return map[string]interface{}{
		"Username":       m.config.Username,
		"Password":       fmt.Sprintf("[REDACTED - %d chars]", len(m.config.Password)),
		"Tokens":         fmt.Sprintf("[%d tokens]", len(m.config.Tokens)),
		"AudioInterval":  m.config.AudioInterval.String(),
		"AudioDataBytes": m.config.AudioDataBytes,
		"AudioFrameSize": m.config.AudioFrameSize(),
		"ClientType":     m.config.ClientType,
	}
}

// getRedactedTLSInfo returns TLS config info with sensitive fields redacted for logging
func (m *MumbleConnectionManager) getRedactedTLSInfo() map[string]interface{} {
	if m.tlsConfig == nil {
		return map[string]interface{}{"tls": "nil"}
	}

	return map[string]interface{}{
		"InsecureSkipVerify": m.tlsConfig.InsecureSkipVerify,
		"ServerName":         m.tlsConfig.ServerName,
		"MinVersion":         m.tlsConfig.MinVersion,
		"MaxVersion":         m.tlsConfig.MaxVersion,
		"CipherSuites":       "[REDACTED]",
		"Certificates":       fmt.Sprintf("[%d certificates]", len(m.tlsConfig.Certificates)),
		"RootCAs":            fmt.Sprintf("[%v]", m.tlsConfig.RootCAs != nil),
		"ClientCAs":          fmt.Sprintf("[%v]", m.tlsConfig.ClientCAs != nil),
	}
}
