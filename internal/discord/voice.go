package discord

import "context"

// VoiceConnection defines the interface for a Discord voice connection.
type VoiceConnection interface {
	// Open connects to the specified voice channel.
	Open(ctx context.Context, channelID string) error
	// Close disconnects from the voice channel.
	Close(ctx context.Context) error
	// SendOpus sends an opus-encoded audio frame.
	SendOpus(data []byte) error
	// ReceiveOpus blocks until the next opus packet is available.
	// Returns nil, nil if the connection is closing.
	ReceiveOpus() (*AudioPacket, error)
	// SetSpeaking sets the speaking status.
	SetSpeaking(ctx context.Context, speaking bool) error
	// IsReady returns true if the voice connection is active and ready.
	IsReady() bool
	// UserIDBySSRC maps an SSRC to a user ID string.
	UserIDBySSRC(ssrc uint32) string
}
