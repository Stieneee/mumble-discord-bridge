package bridgelib

import "github.com/stieneee/mumble-discord-bridge/pkg/logger"

// NewConsoleLogger creates a new console logger
func NewConsoleLogger() Logger {
	return logger.NewConsoleLogger()
}