package logger

import (
	"log"
)

// ConsoleLogger is a simple logger implementation that logs to console
// This is the default logger implementation for standalone usage
type ConsoleLogger struct {
	bridgeID string
}

// NewConsoleLogger creates a new console logger
func NewConsoleLogger() Logger {
	return &ConsoleLogger{}
}

// Debug logs a debug level message to console
func (c *ConsoleLogger) Debug(location, message string) {
	c.logWithLevel("DEBUG", location, message)
}

// Info logs an info level message to console
func (c *ConsoleLogger) Info(location, message string) {
	c.logWithLevel("INFO", location, message)
}

// Warn logs a warning level message to console
func (c *ConsoleLogger) Warn(location, message string) {
	c.logWithLevel("WARN", location, message)
}

// Error logs an error level message to console
func (c *ConsoleLogger) Error(location, message string) {
	c.logWithLevel("ERROR", location, message)
}

// WithBridgeID returns a new logger instance with the specified bridge ID
func (c *ConsoleLogger) WithBridgeID(bridgeID string) Logger {
	return &ConsoleLogger{
		bridgeID: bridgeID,
	}
}

// logWithLevel is a helper method to format and log messages
func (c *ConsoleLogger) logWithLevel(level, location, message string) {
	prefix := "[BRIDGELIB]"
	if c.bridgeID != "" {
		prefix = "[BRIDGELIB:" + c.bridgeID + "]"
	}
	
	log.Printf("%s [%s] [%s] %s", prefix, level, location, message)
}