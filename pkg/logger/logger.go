package logger

// Logger interface defines the logging contract for bridge components
type Logger interface {
	// Debug logs debug-level messages with location and message
	Debug(location, message string)
	
	// Info logs info-level messages with location and message
	Info(location, message string)
	
	// Warn logs warning-level messages with location and message
	Warn(location, message string)
	
	// Error logs error-level messages with location and message
	Error(location, message string)
	
	// WithBridgeID returns a new logger instance that includes the bridge ID in all log messages
	WithBridgeID(bridgeID string) Logger
}