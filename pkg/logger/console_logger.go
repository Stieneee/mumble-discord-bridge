// Package logger provides logging functionality for the bridge.
package logger

import (
	"os"
	"time"

	"github.com/rs/zerolog"
)

// ConsoleLogger is a zerolog-based logger implementation that logs to console
// This is the default logger implementation for standalone usage
type ConsoleLogger struct {
	logger   zerolog.Logger
	bridgeID string
}

// NewConsoleLogger creates a new console logger with zerolog
func NewConsoleLogger() Logger {
	// Configure zerolog for console output with colors and timestamp
	output := zerolog.ConsoleWriter{
		Out:        os.Stdout,
		TimeFormat: time.RFC3339,
	}

	logger := zerolog.New(output).With().Timestamp().Logger()

	return &ConsoleLogger{
		logger: logger,
	}
}

// Debug logs a debug level message to console
func (c *ConsoleLogger) Debug(location, message string) {
	event := c.logger.Debug().Str("location", location)
	if c.bridgeID != "" {
		event = event.Str("bridge_id", c.bridgeID)
	}
	event.Msg(message)
}

// Info logs an info level message to console
func (c *ConsoleLogger) Info(location, message string) {
	event := c.logger.Info().Str("location", location)
	if c.bridgeID != "" {
		event = event.Str("bridge_id", c.bridgeID)
	}
	event.Msg(message)
}

// Warn logs a warning level message to console
func (c *ConsoleLogger) Warn(location, message string) {
	event := c.logger.Warn().Str("location", location)
	if c.bridgeID != "" {
		event = event.Str("bridge_id", c.bridgeID)
	}
	event.Msg(message)
}

// Error logs an error level message to console
func (c *ConsoleLogger) Error(location, message string) {
	event := c.logger.Error().Str("location", location)
	if c.bridgeID != "" {
		event = event.Str("bridge_id", c.bridgeID)
	}
	event.Msg(message)
}

// WithBridgeID returns a new logger instance with the specified bridge ID
func (c *ConsoleLogger) WithBridgeID(bridgeID string) Logger {
	return &ConsoleLogger{
		logger:   c.logger,
		bridgeID: bridgeID,
	}
}
