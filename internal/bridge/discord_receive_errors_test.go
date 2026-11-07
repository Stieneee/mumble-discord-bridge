package bridge

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/stieneee/mumble-discord-bridge/internal/discord"
)

// failingVoiceConn fails every read with err and counts the attempts.
type failingVoiceConn struct {
	mockVoiceConn
	err   error
	reads atomic.Int64
}

func (c *failingVoiceConn) ReceiveOpus() (*discord.AudioPacket, error) {
	c.reads.Add(1)

	return nil, c.err
}

// A voice reconnect closes the UDP socket and leaves it closed until the new
// session is up, which can take up to 30s while the connection still reports
// ready. Reads fail instantly throughout, so without a pause the receive loop
// spins and floods the logs. Other read errors, such as a packet that fails to
// decrypt, are per-packet and must not delay the packets behind them.
func TestDiscordReceivePCM_ReadErrors(t *testing.T) {
	t.Parallel()

	const window = 300 * time.Millisecond

	tests := []struct {
		name     string
		err      error
		minReads int64
		maxReads int64
	}{
		{"closed socket waits", fmt.Errorf("set read deadline: %w", net.ErrClosed), 1, 10},
		{"decrypt failure does not wait", errors.New("failed to DAVE decrypt packet"), 100, 1 << 62},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			conn := &failingVoiceConn{err: tt.err}
			dd := newIntegrationDuplex(t)
			dd.Bridge.DiscordVoiceConnectionManager = &DiscordVoiceConnectionManager{voiceConn: conn}
			conn.setReady(true)

			ctx, cancel := context.WithTimeout(context.Background(), window)
			defer cancel()
			dd.discordReceivePCM(ctx)

			reads := conn.reads.Load()
			assert.GreaterOrEqual(t, reads, tt.minReads, "reads in %v", window)
			assert.LessOrEqual(t, reads, tt.maxReads, "reads in %v", window)
		})
	}
}
