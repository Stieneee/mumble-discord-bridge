package bridge

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/stieneee/mumble-discord-bridge/pkg/logger"
)

// LogEntry represents a captured log entry
type LogEntry struct {
	Level    string
	Location string
	Message  string
	Time     time.Time
}

// MockLogger captures log messages for verification in tests
type MockLogger struct {
	mu      sync.Mutex
	entries []LogEntry
}

// NewMockLogger creates a new mock logger
func NewMockLogger() *MockLogger {
	return &MockLogger{
		entries: make([]LogEntry, 0),
	}
}

// Debug logs debug-level messages
func (m *MockLogger) Debug(location, message string) {
	m.log("DEBUG", location, message)
}

// Info logs info-level messages
func (m *MockLogger) Info(location, message string) {
	m.log("INFO", location, message)
}

// Warn logs warning-level messages
func (m *MockLogger) Warn(location, message string) {
	m.log("WARN", location, message)
}

// Error logs error-level messages
func (m *MockLogger) Error(location, message string) {
	m.log("ERROR", location, message)
}

// WithBridgeID returns self (mock doesn't need bridge ID)
func (m *MockLogger) WithBridgeID(_ string) logger.Logger {
	return m
}

func (m *MockLogger) log(level, location, message string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = append(m.entries, LogEntry{
		Level:    level,
		Location: location,
		Message:  message,
		Time:     time.Now(),
	})
}

// GetEntries returns all captured log entries
func (m *MockLogger) GetEntries() []LogEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]LogEntry, len(m.entries))
	copy(result, m.entries)
	return result
}

// GetEntriesByLevel returns entries filtered by log level
func (m *MockLogger) GetEntriesByLevel(level string) []LogEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []LogEntry
	for _, e := range m.entries {
		if e.Level == level {
			result = append(result, e)
		}
	}
	return result
}

// Clear clears all log entries
func (m *MockLogger) Clear() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = m.entries[:0]
}

// ContainsMessage checks if any log entry contains the given message substring
func (m *MockLogger) ContainsMessage(substr string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range m.entries {
		if containsSubstring(e.Message, substr) {
			return true
		}
	}
	return false
}

// EmittedEvent represents an event emitted by the mock event emitter
type EmittedEvent struct {
	Service   string
	EventType int
	Connected bool
	Error     error
	Time      time.Time
}

// MockBridgeEventEmitter tracks emitted bridge events
type MockBridgeEventEmitter struct {
	mu     sync.Mutex
	events []EmittedEvent
}

// NewMockBridgeEventEmitter creates a new mock event emitter
func NewMockBridgeEventEmitter() *MockBridgeEventEmitter {
	return &MockBridgeEventEmitter{
		events: make([]EmittedEvent, 0),
	}
}

// EmitConnectionEvent records a connection event
func (m *MockBridgeEventEmitter) EmitConnectionEvent(service string, eventType int, connected bool, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, EmittedEvent{
		Service:   service,
		EventType: eventType,
		Connected: connected,
		Error:     err,
		Time:      time.Now(),
	})
}

// GetEvents returns all emitted events
func (m *MockBridgeEventEmitter) GetEvents() []EmittedEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]EmittedEvent, len(m.events))
	copy(result, m.events)
	return result
}

// GetEventsByService returns events filtered by service
func (m *MockBridgeEventEmitter) GetEventsByService(service string) []EmittedEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []EmittedEvent
	for _, e := range m.events {
		if e.Service == service {
			result = append(result, e)
		}
	}
	return result
}

// Clear clears all events
func (m *MockBridgeEventEmitter) Clear() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = m.events[:0]
}

// MockConnectionManager is a controllable connection manager for testing
type MockConnectionManager struct {
	status       ConnectionStatus
	statusMu     sync.RWMutex
	eventChan    chan ConnectionEvent
	shouldFail   bool
	connectDelay time.Duration
	stopCalled   int32
	startCalled  int32
	ctx          context.Context
	cancel       context.CancelFunc
	closedChan   chan struct{}
	stopOnce     sync.Once
}

// NewMockConnectionManager creates a new mock connection manager
func NewMockConnectionManager() *MockConnectionManager {
	return &MockConnectionManager{
		status:     ConnectionDisconnected,
		eventChan:  make(chan ConnectionEvent, 100),
		closedChan: make(chan struct{}),
	}
}

// Start simulates starting the connection manager
func (m *MockConnectionManager) Start(ctx context.Context) error {
	atomic.AddInt32(&m.startCalled, 1)
	m.ctx, m.cancel = context.WithCancel(ctx)

	if m.shouldFail {
		m.SetStatus(ConnectionFailed, nil)
		return nil
	}

	// Simulate connection delay
	if m.connectDelay > 0 {
		select {
		case <-time.After(m.connectDelay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	m.SetStatus(ConnectionConnected, nil)
	return nil
}

// Stop simulates stopping the connection manager
func (m *MockConnectionManager) Stop() error {
	atomic.AddInt32(&m.stopCalled, 1)
	m.stopOnce.Do(func() {
		if m.cancel != nil {
			m.cancel()
		}
		close(m.closedChan)
		close(m.eventChan)
	})
	return nil
}

// IsConnected returns whether the connection is established
func (m *MockConnectionManager) IsConnected() bool {
	return m.GetStatus() == ConnectionConnected
}

// GetStatus returns the current connection status
func (m *MockConnectionManager) GetStatus() ConnectionStatus {
	m.statusMu.RLock()
	defer m.statusMu.RUnlock()
	return m.status
}

// SetStatus updates the connection status and emits an event
func (m *MockConnectionManager) SetStatus(status ConnectionStatus, err error) {
	select {
	case <-m.closedChan:
		return
	default:
	}

	m.statusMu.Lock()
	oldStatus := m.status
	m.status = status
	m.statusMu.Unlock()

	if oldStatus != status {
		event := ConnectionEvent{
			Type:   m.statusToEventType(status),
			Status: status,
			Error:  err,
		}

		select {
		case <-m.closedChan:
			return
		default:
		}

		select {
		case m.eventChan <- event:
		default:
			// Channel full
		}
	}
}

// GetEventChannel returns the event channel
func (m *MockConnectionManager) GetEventChannel() <-chan ConnectionEvent {
	return m.eventChan
}

// SetShouldFail configures whether connection should fail
func (m *MockConnectionManager) SetShouldFail(fail bool) {
	m.shouldFail = fail
}

// SetConnectDelay sets the simulated connection delay
func (m *MockConnectionManager) SetConnectDelay(d time.Duration) {
	m.connectDelay = d
}

// GetStartCallCount returns the number of times Start was called
func (m *MockConnectionManager) GetStartCallCount() int {
	return int(atomic.LoadInt32(&m.startCalled))
}

// GetStopCallCount returns the number of times Stop was called
func (m *MockConnectionManager) GetStopCallCount() int {
	return int(atomic.LoadInt32(&m.stopCalled))
}

func (m *MockConnectionManager) statusToEventType(status ConnectionStatus) ConnectionEventType {
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

// Helper function for string contains check
func containsSubstring(s, substr string) bool {
	if substr == "" {
		return true
	}
	if len(s) < len(substr) {
		return false
	}
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
