package discord

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/snowflake/v2"
)

// receiveDeadline is the read timeout for ReceiveOpus. It allows the goroutine
// to periodically re-check the connection state instead of blocking forever.
const receiveDeadline = 100 * time.Millisecond

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
// The write lock is held only for state assignments, not for the blocking
// conn.Open() call, so concurrent readers (SendOpus, ReceiveOpus, IsReady)
// are not stalled during the potentially slow connection handshake.
func (vc *DisgoVoiceConnection) Open(ctx context.Context, channelID string) error {
	cid, err := snowflake.Parse(channelID)
	if err != nil {
		return fmt.Errorf("invalid channel ID %s: %w", channelID, err)
	}

	conn := vc.client.VoiceManager.CreateConn(vc.guildID)

	// Perform the blocking open without holding the lock.
	if err := conn.Open(ctx, cid, false, false); err != nil {
		return fmt.Errorf("failed to open voice connection: %w", err)
	}

	// Publish the ready connection atomically.
	vc.mu.Lock()
	vc.conn = conn
	vc.closed = false
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
//
// RTP framing is handled by disgo's UDPConn.Write: each call increments the
// RTP sequence number by 1 and the timestamp by 960 (one 20ms Opus frame).
// The caller (toDiscordSender) is responsible for sending at 20ms intervals
// and for sending silence frames during gaps to keep the RTP timestamp
// advancing in sync with wall-clock time.
func (vc *DisgoVoiceConnection) SendOpus(data []byte) error {
	vc.mu.RLock()
	conn := vc.conn
	ready := vc.ready
	vc.mu.RUnlock()

	if conn == nil || !ready {
		return fmt.Errorf("voice connection not ready (guild %s)", vc.guildID)
	}

	_, err := conn.UDP().Write(data)

	return err
}

// ReceiveOpus blocks until the next opus packet is available.
// A short read deadline is set so the caller can periodically re-check
// connection state and context cancellation instead of blocking forever.
func (vc *DisgoVoiceConnection) ReceiveOpus() (*AudioPacket, error) {
	vc.mu.RLock()
	conn := vc.conn
	ready := vc.ready
	closed := vc.closed
	vc.mu.RUnlock()

	if conn == nil || !ready || closed {
		return nil, nil
	}

	// Set a short read deadline so the goroutine can re-check state when no
	// packets arrive. Timeout errors are returned as nil, nil to the caller.
	_ = conn.UDP().SetReadDeadline(time.Now().Add(receiveDeadline))

	pkt, err := conn.UDP().ReadPacket()
	if err != nil {
		// Deadline exceeded is normal — return nil so the caller loops back
		// and re-checks connection/context state.
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			return nil, nil
		}
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
