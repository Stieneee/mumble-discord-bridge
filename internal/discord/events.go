package discord

// EventHandler receives Discord events in a library-agnostic way.
type EventHandler interface {
	OnReady()
	OnGuildCreate(guild *Guild)
	OnMessageCreate(msg *Message)
	OnVoiceStateUpdate(state *VoiceState)
}
