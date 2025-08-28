package bridge

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/stieneee/mumble-discord-bridge/pkg/logger"
)

// BridgeEventEmitter defines interface for emitting bridge events
type BridgeEventEmitter interface { //nolint:revive // API consistency: keeping Bridge prefix for public types
	EmitConnectionEvent(service string, eventType int, connected bool, err error)
}

// ConnectionStatus represents the status of a connection
type ConnectionStatus int

const (
	// ConnectionDisconnected indicates the service is disconnected.
	ConnectionDisconnected ConnectionStatus = iota
	// ConnectionConnecting indicates the service is attempting to connect.
	ConnectionConnecting
	// ConnectionConnected indicates the service is connected.
	ConnectionConnected
	// ConnectionReconnecting indicates the service is attempting to reconnect.
	ConnectionReconnecting
	// ConnectionFailed indicates the service connection has failed.
	ConnectionFailed
)

func (c ConnectionStatus) String() string {
	switch c {
	case ConnectionDisconnected:
		return "Disconnected"
	case ConnectionConnecting:
		return "Connecting"
	case ConnectionConnected:
		return "Connected"
	case ConnectionReconnecting:
		return "Reconnecting"
	case ConnectionFailed:
		return "Failed"
	default:
		return "Unknown"
	}
}

// ConnectionEvent represents a connection state change event
type ConnectionEvent struct {
	Type     ConnectionEventType
	Status   ConnectionStatus
	Error    error
	Metadata map[string]interface{}
}

// ConnectionEventType represents the type of connection event
type ConnectionEventType int

const (
	// EventConnecting indicates a connection attempt is starting.
	EventConnecting ConnectionEventType = iota
	// EventConnected indicates a successful connection.
	EventConnected
	// EventDisconnected indicates the connection has been lost.
	EventDisconnected
	// EventReconnecting indicates a reconnection attempt is starting.
	EventReconnecting
	// EventFailed indicates the connection has failed.
	EventFailed
	// EventHealthCheck indicates a health check event.
	EventHealthCheck
)

func (c ConnectionEventType) String() string {
	switch c {
	case EventConnecting:
		return "Connecting"
	case EventConnected:
		return "Connected"
	case EventDisconnected:
		return "Disconnected"
	case EventReconnecting:
		return "Reconnecting"
	case EventFailed:
		return "Failed"
	case EventHealthCheck:
		return "HealthCheck"
	default:
		return "Unknown"
	}
}

// ConnectionManager defines the interface for managing a single connection type
type ConnectionManager interface {
	// Start begins the connection process
	Start(ctx context.Context) error

	// Stop gracefully shuts down the connection
	Stop() error

	// IsConnected returns true if the connection is established and healthy
	IsConnected() bool

	// GetStatus returns the current connection status
	GetStatus() ConnectionStatus

	// GetEventChannel returns a channel for connection events
	GetEventChannel() <-chan ConnectionEvent

	// GetConnectionInfo returns connection-specific information
	GetConnectionInfo() map[string]interface{}
}

// BaseConnectionManager provides common functionality for connection managers
type BaseConnectionManager struct {
	status      ConnectionStatus
	statusMutex sync.RWMutex
	eventChan   chan ConnectionEvent
	logger      logger.Logger
	ctx         context.Context
	cancel      context.CancelFunc

	// Health check settings
	healthCheckInterval time.Duration

	// Bridge event emission
	eventEmitter BridgeEventEmitter
	serviceName  string

	// Stop synchronization
	stopOnce sync.Once
	stopped  bool
}

// NewBaseConnectionManager creates a new base connection manager
func NewBaseConnectionManager(logger logger.Logger, serviceName string, eventEmitter BridgeEventEmitter) *BaseConnectionManager {
	return &BaseConnectionManager{
		status:              ConnectionDisconnected,
		eventChan:           make(chan ConnectionEvent, 100), // Increased buffer to prevent blocking
		logger:              logger,
		ctx:                 nil, // Will be set when Start() is called with parent context
		cancel:              nil,
		healthCheckInterval: time.Second * 30,
		eventEmitter:        eventEmitter,
		serviceName:         serviceName,
	}
}

// SetStatus updates the connection status and sends an event
func (b *BaseConnectionManager) SetStatus(status ConnectionStatus, err error) {
	b.statusMutex.Lock()
	oldStatus := b.status
	b.status = status
	b.statusMutex.Unlock()

	if oldStatus != status {
		eventType := b.statusToEventType(status)
		event := ConnectionEvent{
			Type:   eventType,
			Status: status,
			Error:  err,
		}

		select {
		case b.eventChan <- event:
		default:
			// Event channel is full - try to drop oldest event and add new one
			select {
			case <-b.eventChan:
				// Successfully dropped oldest event, try to add new one
				select {
				case b.eventChan <- event:
				default:
					b.logger.Warn("CONNECTION", "Event channel still full after dropping oldest event")
				}
			default:
				b.logger.Warn("CONNECTION", "Event channel full or closed, dropping event")
			}
		}

		// Emit bridge-level event if emitter is available
		if b.eventEmitter != nil {
			var bridgeEventType int
			var connected bool

			switch status {
			case ConnectionConnecting:
				bridgeEventType = 0 // EventDiscordConnecting or EventMumbleConnecting
				connected = false
			case ConnectionConnected:
				bridgeEventType = 1 // EventDiscordConnected or EventMumbleConnected
				connected = true
			case ConnectionDisconnected:
				bridgeEventType = 2 // EventDiscordDisconnected or EventMumbleDisconnected
				connected = false
			case ConnectionReconnecting:
				bridgeEventType = 3 // EventDiscordReconnecting or EventMumbleReconnecting
				connected = false
			case ConnectionFailed:
				bridgeEventType = 4 // EventDiscordConnectionFailed or EventMumbleConnectionFailed
				connected = false
			}

			b.eventEmitter.EmitConnectionEvent(b.serviceName, bridgeEventType, connected, err)
		}

		b.logger.Debug("CONNECTION", fmt.Sprintf("%s status changed: %s -> %s", b.serviceName, oldStatus, status))
	}
}

// GetStatus returns the current connection status
func (b *BaseConnectionManager) GetStatus() ConnectionStatus {
	b.statusMutex.RLock()
	defer b.statusMutex.RUnlock()

	return b.status
}

// IsConnected returns true if the connection is established
func (b *BaseConnectionManager) IsConnected() bool {
	return b.GetStatus() == ConnectionConnected
}

// GetEventChannel returns the event channel
func (b *BaseConnectionManager) GetEventChannel() <-chan ConnectionEvent {
	return b.eventChan
}

// InitContext initializes the context for the connection manager
func (b *BaseConnectionManager) InitContext(parentCtx context.Context) {
	b.ctx, b.cancel = context.WithCancel(parentCtx)
}

// Stop cancels the context and sets status to disconnected
func (b *BaseConnectionManager) Stop() error {
	// Use sync.Once to ensure Stop logic only executes once
	b.stopOnce.Do(func() {
		// Check if already stopped (additional safety)
		b.statusMutex.Lock()
		if b.stopped {
			b.statusMutex.Unlock()

			return
		}
		b.stopped = true
		b.statusMutex.Unlock()

		if b.cancel != nil {
			b.cancel()
		}
		b.SetStatus(ConnectionDisconnected, nil)

		// Close the event channel to signal no more events will be sent
		go func() {
			// Small delay to allow any pending SetStatus calls to complete
			time.Sleep(100 * time.Millisecond)
			close(b.eventChan)
		}()
	})

	return nil
}

// statusToEventType converts a ConnectionStatus to ConnectionEventType
func (b *BaseConnectionManager) statusToEventType(status ConnectionStatus) ConnectionEventType {
	switch status {
	case ConnectionConnecting:
		return EventConnecting
	case ConnectionConnected:
		return EventConnected
	case ConnectionDisconnected:
		return EventDisconnected
	case ConnectionReconnecting:
		return EventReconnecting
	case ConnectionFailed:
		return EventFailed
	default:
		return EventDisconnected
	}
}

// Note: DiscordConnectionManager and MumbleConnectionManager are defined in their respective files

// ConnectionManagerConfig holds configuration for connection managers
type ConnectionManagerConfig struct {
	MaxRetries          int           `json:"maxRetries"`
	BaseRetryDelay      time.Duration `json:"baseRetryDelay"`
	MaxRetryDelay       time.Duration `json:"maxRetryDelay"`
	RetryMultiplier     float64       `json:"retryMultiplier"`
	HealthCheckInterval time.Duration `json:"healthCheckInterval"`
}

// DefaultConnectionManagerConfig returns default configuration
func DefaultConnectionManagerConfig() *ConnectionManagerConfig {
	return &ConnectionManagerConfig{
		MaxRetries:          5,
		BaseRetryDelay:      time.Second * 2,
		MaxRetryDelay:       time.Minute * 2,
		RetryMultiplier:     2.0,
		HealthCheckInterval: time.Second * 30,
	}
}
