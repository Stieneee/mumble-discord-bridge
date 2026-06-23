package bridge

import (
	"strings"
	"sync"
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
		if strings.Contains(e.Message, substr) {
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

// NOTE: MockConnectionManager was removed — it was unused after the connection
// manager refactoring. The connection_manager_test.go tests use the real
// ConnectionManager with mock Discord/Mumble clients instead.
