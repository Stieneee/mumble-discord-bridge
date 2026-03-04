// Package discord provides library-agnostic types and interfaces for Discord integration.
package discord

// AudioPacket represents a decoded Discord voice packet.
type AudioPacket struct {
	SSRC      uint32
	Sequence  uint16
	Timestamp uint32
	Opus      []byte
}

// VoiceState represents a user's voice connection state.
type VoiceState struct {
	UserID    string
	ChannelID string
	GuildID   string
}

// User represents a Discord user.
type User struct {
	ID       string
	Username string
	Bot      bool
}

// Message represents a Discord message.
type Message struct {
	ID        string
	ChannelID string
	GuildID   string
	Content   string
	Author    User
}

// Guild represents a Discord guild with voice states.
type Guild struct {
	ID          string
	Name        string
	VoiceStates []VoiceState
}
