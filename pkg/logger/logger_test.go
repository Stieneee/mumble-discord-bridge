package logger

import (
	"strings"
	"testing"
)

// mockLogger is a simple implementation for testing the interface
type mockLogger struct {
	debugs []string
	infos  []string
	warns  []string
	errors []string
	bridgeID string
}

func (m *mockLogger) Debug(location, message string) {
	m.debugs = append(m.debugs, location+": "+message)
}

func (m *mockLogger) Info(location, message string) {
	m.infos = append(m.infos, location+": "+message)
}

func (m *mockLogger) Warn(location, message string) {
	m.warns = append(m.warns, location+": "+message)
}

func (m *mockLogger) Error(location, message string) {
	m.errors = append(m.errors, location+": "+message)
}

func (m *mockLogger) WithBridgeID(bridgeID string) Logger {
	return &mockLogger{
		bridgeID: bridgeID,
	}
}

// TestLoggerInterface verifies the Logger interface is satisfied
func TestLoggerInterface(t *testing.T) {
	// This test ensures mockLogger implements Logger interface
	var _ Logger = (*mockLogger)(nil)
}

// TestMockLoggerDebug tests the debug method
func TestMockLoggerDebug(t *testing.T) {
	logger := &mockLogger{}
	logger.Debug("test-location", "test-message")

	if len(logger.debugs) != 1 {
		t.Fatalf("expected 1 debug message, got %d", len(logger.debugs))
	}
	if !strings.Contains(logger.debugs[0], "test-location") {
		t.Errorf("expected debug to contain 'test-location', got %s", logger.debugs[0])
	}
	if !strings.Contains(logger.debugs[0], "test-message") {
		t.Errorf("expected debug to contain 'test-message', got %s", logger.debugs[0])
	}
}

// TestMockLoggerInfo tests the info method
func TestMockLoggerInfo(t *testing.T) {
	logger := &mockLogger{}
	logger.Info("info-loc", "info-msg")

	if len(logger.infos) != 1 {
		t.Fatalf("expected 1 info message, got %d", len(logger.infos))
	}
	if !strings.Contains(logger.infos[0], "info-loc") {
		t.Errorf("expected info to contain 'info-loc', got %s", logger.infos[0])
	}
}

// TestMockLoggerWarn tests the warn method
func TestMockLoggerWarn(t *testing.T) {
	logger := &mockLogger{}
	logger.Warn("warn-loc", "warn-msg")

	if len(logger.warns) != 1 {
		t.Fatalf("expected 1 warn message, got %d", len(logger.warns))
	}
	if !strings.Contains(logger.warns[0], "warn-loc") {
		t.Errorf("expected warn to contain 'warn-loc', got %s", logger.warns[0])
	}
}

// TestMockLoggerError tests the error method
func TestMockLoggerError(t *testing.T) {
	logger := &mockLogger{}
	logger.Error("err-loc", "err-msg")

	if len(logger.errors) != 1 {
		t.Fatalf("expected 1 error message, got %d", len(logger.errors))
	}
	if !strings.Contains(logger.errors[0], "err-loc") {
		t.Errorf("expected error to contain 'err-loc', got %s", logger.errors[0])
	}
}

// TestWithBridgeID tests the WithBridgeID method returns a new logger
func TestWithBridgeID(t *testing.T) {
	logger := &mockLogger{}
	newLogger := logger.WithBridgeID("bridge-123")

	if newLogger == logger {
		t.Error("WithBridgeID should return a new logger instance")
	}

	// Type assert to check bridgeID
	mockNew, ok := newLogger.(*mockLogger)
	if !ok {
		t.Fatal("expected *mockLogger type")
	}
	if mockNew.bridgeID != "bridge-123" {
		t.Errorf("expected bridgeID 'bridge-123', got %s", mockNew.bridgeID)
	}
}

// TestMultipleMessages tests multiple log calls accumulate correctly
func TestMultipleMessages(t *testing.T) {
	logger := &mockLogger{}

	for i := 0; i < 5; i++ {
		logger.Info("loc", "msg")
	}

	if len(logger.infos) != 5 {
		t.Errorf("expected 5 info messages, got %d", len(logger.infos))
	}
}

// TestMixedLogLevels tests that different log levels don't interfere
func TestMixedLogLevels(t *testing.T) {
	logger := &mockLogger{}
	logger.Debug("d", "debug")
	logger.Info("i", "info")
	logger.Warn("w", "warn")
	logger.Error("e", "error")

	if len(logger.debugs) != 1 || len(logger.infos) != 1 || len(logger.warns) != 1 || len(logger.errors) != 1 {
		t.Errorf("expected 1 message per level, got debug=%d, info=%d, warn=%d, error=%d",
			len(logger.debugs), len(logger.infos), len(logger.warns), len(logger.errors))
	}
}
