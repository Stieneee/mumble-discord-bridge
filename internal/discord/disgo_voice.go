package discord

import (
	"context"
	"fmt"
	"sync"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/snowflake/v2"
)

// DisgoVoiceConnection implements VoiceConnection using disgo's voice system.
type DisgoVoiceConnection struct {
	client  *bot.Client
	guildID snowflake.ID
	conn    voice.Conn
	mu      sync.RWMutex
	ready   bool
	closed  bool
}

// Open connects to the specified voice channel.
func (vc *DisgoVoiceConnection) Open(ctx context.Context, _, channelID string) error {
	cid, err := snowflake.Parse(channelID)
	if err != nil {
		return fmt.Errorf("invalid channel ID %s: %w", channelID, err)
	}

	vc.mu.Lock()
	vc.conn = vc.client.VoiceManager.CreateConn(vc.guildID)
	vc.closed = false
	vc.mu.Unlock()

	if err := vc.conn.Open(ctx, cid, false, false); err != nil {
		return fmt.Errorf("failed to open voice connection: %w", err)
	}

	vc.mu.Lock()
	vc.ready = true
	vc.mu.Unlock()

	return nil
}

// Close disconnects from the voice channel.
func (vc *DisgoVoiceConnection) Close(ctx context.Context) error {
	vc.mu.Lock()
	defer vc.mu.Unlock()

	if vc.conn == nil || vc.closed {
		return nil
	}

	vc.closed = true
	vc.ready = false
	vc.conn.Close(ctx)
	vc.client.VoiceManager.RemoveConn(vc.guildID)

	return nil
}

// SendOpus sends an opus-encoded audio frame via UDP.
func (vc *DisgoVoiceConnection) SendOpus(data []byte) error {
	vc.mu.RLock()
	conn := vc.conn
	ready := vc.ready
	vc.mu.RUnlock()

	if conn == nil || !ready {
		return fmt.Errorf("voice connection not ready")
	}

	_, err := conn.UDP().Write(data)

	return err
}

// ReceiveOpus blocks until the next opus packet is available.
func (vc *DisgoVoiceConnection) ReceiveOpus() (*AudioPacket, error) {
	vc.mu.RLock()
	conn := vc.conn
	ready := vc.ready
	closed := vc.closed
	vc.mu.RUnlock()

	if conn == nil || !ready || closed {
		return nil, nil
	}

	pkt, err := conn.UDP().ReadPacket()
	if err != nil {
		return nil, err
	}

	return &AudioPacket{
		SSRC:      pkt.SSRC,
		Sequence:  pkt.Sequence,
		Timestamp: pkt.Timestamp,
		Opus:      pkt.Opus,
	}, nil
}

// SetSpeaking sets the speaking status.
func (vc *DisgoVoiceConnection) SetSpeaking(ctx context.Context, speaking bool) error {
	vc.mu.RLock()
	conn := vc.conn
	ready := vc.ready
	vc.mu.RUnlock()

	if conn == nil || !ready {
		return nil
	}

	flags := voice.SpeakingFlagNone
	if speaking {
		flags = voice.SpeakingFlagMicrophone
	}

	return conn.SetSpeaking(ctx, flags)
}

// IsReady returns true if the voice connection is active and ready.
func (vc *DisgoVoiceConnection) IsReady() bool {
	vc.mu.RLock()
	defer vc.mu.RUnlock()

	return vc.ready && !vc.closed && vc.conn != nil
}

// UserIDBySSRC maps an SSRC to a user ID string.
func (vc *DisgoVoiceConnection) UserIDBySSRC(ssrc uint32) string {
	vc.mu.RLock()
	conn := vc.conn
	vc.mu.RUnlock()

	if conn == nil {
		return ""
	}

	uid := conn.UserIDBySSRC(ssrc)
	if uid == 0 {
		return ""
	}

	return uid.String()
}
