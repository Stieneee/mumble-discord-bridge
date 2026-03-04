package bridge

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	discord "github.com/stieneee/mumble-discord-bridge/internal/discord"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Mock Discord client
// ---------------------------------------------------------------------------

type mockDiscordClient struct {
	botUserID    string
	guild        *discord.Guild
	ready        bool
	mu           sync.Mutex
	sentMessages []struct{ channelID, content string }
}

func (m *mockDiscordClient) Connect(_ context.Context) error { return nil }

func (m *mockDiscordClient) Disconnect(_ context.Context) error { return nil }

func (m *mockDiscordClient) SendMessage(channelID, content string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sentMessages = append(m.sentMessages, struct{ channelID, content string }{channelID, content})
	return nil
}

func (m *mockDiscordClient) GetUser(userID string) (*discord.User, error) {
	return &discord.User{ID: userID, Username: "user-" + userID}, nil
}

func (m *mockDiscordClient) CreateDM(userID string) (string, error) {
	return "dm-" + userID, nil
}

func (m *mockDiscordClient) GetGuild(guildID string) (*discord.Guild, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.guild != nil && m.guild.ID == guildID {
		return m.guild, nil
	}
	return nil, fmt.Errorf("guild not found")
}

func (m *mockDiscordClient) GetBotUserID() string { return m.botUserID }

func (m *mockDiscordClient) IsReady() bool { return m.ready }

func (m *mockDiscordClient) CreateVoiceConnection(_ string) discord.VoiceConnection { return nil }

func (m *mockDiscordClient) SetEventHandler(_ discord.EventHandler) {}

// getSentMessages returns a snapshot of sent messages.
func (m *mockDiscordClient) getSentMessages() []struct{ channelID, content string } {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]struct{ channelID, content string }, len(m.sentMessages))
	copy(out, m.sentMessages)
	return out
}

// ---------------------------------------------------------------------------
// Helper: create a BridgeState wired to the mock Discord client
// ---------------------------------------------------------------------------

func setupDiscordHandlerTest(voiceStates []discord.VoiceState) (*DiscordListener, *BridgeState, *mockDiscordClient) {
	mockLog := NewMockLogger()
	bs := createTestBridgeState(mockLog)

	mc := &mockDiscordClient{
		botUserID: "bot-user-id",
		guild: &discord.Guild{
			ID:          bs.BridgeConfig.GID,
			Name:        "Test Guild",
			VoiceStates: voiceStates,
		},
		ready: true,
	}
	bs.DiscordClient = mc
	bs.DiscordChannelID = bs.BridgeConfig.CID

	listener := &DiscordListener{Bridge: bs}
	return listener, bs, mc
}

// ===========================================================================
// 1. TestVoiceUpdate_LockOrderNoDeadlock
// ===========================================================================

func TestVoiceUpdate_LockOrderNoDeadlock(t *testing.T) {
	l, bs, _ := setupDiscordHandlerTest([]discord.VoiceState{
		{UserID: "u1", ChannelID: "test-channel-id", GuildID: "test-guild-id"},
	})

	assertNoDeadlock(t, 5*time.Second, func() {
		var wg sync.WaitGroup

		// 50 concurrent VoiceStateUpdate calls
		for i := 0; i < 50; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				l.OnVoiceStateUpdate(&discord.VoiceState{
					UserID:    fmt.Sprintf("user-%d", idx),
					ChannelID: bs.BridgeConfig.CID,
					GuildID:   bs.BridgeConfig.GID,
				})
			}(i)
		}

		// 50 concurrent BridgeMutex lock/unlock cycles
		for i := 0; i < 50; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				bs.BridgeMutex.Lock()
				_ = bs.Connected
				bs.BridgeMutex.Unlock()
			}()
		}

		wg.Wait()
	})
}

// ===========================================================================
// 2. TestVoiceUpdate_ConcurrentUserJoinLeave
// ===========================================================================

func TestVoiceUpdate_ConcurrentUserJoinLeave(t *testing.T) {
	l, bs, mc := setupDiscordHandlerTest(nil)

	// Build a guild that has 75 users (first 50 present, last 25 absent for removals)
	states := make([]discord.VoiceState, 0, 50)
	for i := 0; i < 50; i++ {
		states = append(states, discord.VoiceState{
			UserID:    fmt.Sprintf("user-%d", i),
			ChannelID: bs.BridgeConfig.CID,
			GuildID:   bs.BridgeConfig.GID,
		})
	}

	mc.mu.Lock()
	mc.guild.VoiceStates = states
	mc.mu.Unlock()

	// 50 goroutines trigger join events
	runConcurrentlyWithTimeout(t, 10*time.Second, 50, func(i int) {
		l.OnVoiceStateUpdate(&discord.VoiceState{
			UserID:    fmt.Sprintf("user-%d", i),
			ChannelID: bs.BridgeConfig.CID,
			GuildID:   bs.BridgeConfig.GID,
		})
	})

	// Now remove 25 users from the guild voice states and trigger updates
	mc.mu.Lock()
	mc.guild.VoiceStates = states[:25]
	mc.mu.Unlock()

	runConcurrentlyWithTimeout(t, 10*time.Second, 25, func(i int) {
		l.OnVoiceStateUpdate(&discord.VoiceState{
			UserID:    fmt.Sprintf("user-%d", i+25),
			ChannelID: "", // user left
			GuildID:   bs.BridgeConfig.GID,
		})
	})

	// After removals, only the first 25 should remain
	bs.DiscordUsersMutex.Lock()
	count := len(bs.DiscordUsers)
	bs.DiscordUsersMutex.Unlock()

	assert.Equal(t, 25, count, "expected 25 users remaining after concurrent join/leave")
}

// ===========================================================================
// 3. TestVoiceUpdate_MapIteratorNotInvalidated
// ===========================================================================

func TestVoiceUpdate_MapIteratorNotInvalidated(t *testing.T) {
	l, bs, mc := setupDiscordHandlerTest(nil)

	// Seed 20 users
	bs.DiscordUsersMutex.Lock()
	for i := 0; i < 20; i++ {
		bs.DiscordUsers[fmt.Sprintf("user-%d", i)] = DiscordUser{
			username: fmt.Sprintf("user-user-%d", i),
			seen:     true,
			dmID:     fmt.Sprintf("dm-user-%d", i),
		}
	}
	bs.DiscordUsersMutex.Unlock()

	// Guild still has the same 20 users
	states := make([]discord.VoiceState, 20)
	for i := 0; i < 20; i++ {
		states[i] = discord.VoiceState{
			UserID:    fmt.Sprintf("user-%d", i),
			ChannelID: bs.BridgeConfig.CID,
			GuildID:   bs.BridgeConfig.GID,
		}
	}
	mc.mu.Lock()
	mc.guild.VoiceStates = states
	mc.mu.Unlock()

	// Concurrent iteration (mark unseen) + modification (voice state updates)
	assertNoDeadlock(t, 5*time.Second, func() {
		var wg sync.WaitGroup

		// Goroutine 1: iterate and mark all unseen
		wg.Add(1)
		go func() {
			defer wg.Done()
			bs.DiscordUsersMutex.Lock()
			for u := range bs.DiscordUsers {
				du := bs.DiscordUsers[u]
				du.seen = false
				bs.DiscordUsers[u] = du
			}
			bs.DiscordUsersMutex.Unlock()
		}()

		// Goroutine 2+: concurrent voice state updates
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				l.OnVoiceStateUpdate(&discord.VoiceState{
					UserID:    fmt.Sprintf("user-%d", idx),
					ChannelID: bs.BridgeConfig.CID,
					GuildID:   bs.BridgeConfig.GID,
				})
			}(i)
		}

		wg.Wait()
	})
}

// ===========================================================================
// 4. TestVoiceUpdate_GuildIDMismatch
// ===========================================================================

func TestVoiceUpdate_GuildIDMismatch(t *testing.T) {
	l, bs, _ := setupDiscordHandlerTest(nil)

	// Trigger with a wrong guild ID
	l.OnVoiceStateUpdate(&discord.VoiceState{
		UserID:    "user-1",
		ChannelID: bs.BridgeConfig.CID,
		GuildID:   "wrong-guild-id",
	})

	// No users should have been added
	bs.DiscordUsersMutex.Lock()
	count := len(bs.DiscordUsers)
	bs.DiscordUsersMutex.Unlock()

	assert.Equal(t, 0, count, "voice update with wrong guild ID should be ignored")
}

// ===========================================================================
// 5. TestVoiceUpdate_BotUserIgnored
// ===========================================================================

func TestVoiceUpdate_BotUserIgnored(t *testing.T) {
	l, bs, mc := setupDiscordHandlerTest(nil)

	// Put the bot itself in the guild voice states
	mc.mu.Lock()
	mc.guild.VoiceStates = []discord.VoiceState{
		{UserID: mc.botUserID, ChannelID: bs.BridgeConfig.CID, GuildID: bs.BridgeConfig.GID},
	}
	mc.mu.Unlock()

	l.OnVoiceStateUpdate(&discord.VoiceState{
		UserID:    mc.botUserID,
		ChannelID: bs.BridgeConfig.CID,
		GuildID:   bs.BridgeConfig.GID,
	})

	bs.DiscordUsersMutex.Lock()
	_, exists := bs.DiscordUsers[mc.botUserID]
	bs.DiscordUsersMutex.Unlock()

	assert.False(t, exists, "bot user should not be tracked in DiscordUsers")
}

// ===========================================================================
// 6. TestVoiceUpdate_UserSeenTracking
// ===========================================================================

func TestVoiceUpdate_UserSeenTracking(t *testing.T) {
	l, bs, mc := setupDiscordHandlerTest(nil)

	// Seed two users already marked seen
	bs.DiscordUsersMutex.Lock()
	bs.DiscordUsers["u1"] = DiscordUser{username: "user-u1", seen: true, dmID: "dm-u1"}
	bs.DiscordUsers["u2"] = DiscordUser{username: "user-u2", seen: true, dmID: "dm-u2"}
	bs.DiscordUsersMutex.Unlock()

	// Guild only reports u1 in the channel (u2 left)
	mc.mu.Lock()
	mc.guild.VoiceStates = []discord.VoiceState{
		{UserID: "u1", ChannelID: bs.BridgeConfig.CID, GuildID: bs.BridgeConfig.GID},
	}
	mc.mu.Unlock()

	l.OnVoiceStateUpdate(&discord.VoiceState{
		UserID:    "u2",
		ChannelID: "",
		GuildID:   bs.BridgeConfig.GID,
	})

	// u1 should still be present, u2 should be removed
	bs.DiscordUsersMutex.Lock()
	_, u1Exists := bs.DiscordUsers["u1"]
	_, u2Exists := bs.DiscordUsers["u2"]
	bs.DiscordUsersMutex.Unlock()

	assert.True(t, u1Exists, "u1 should remain after mark-all-unseen + re-seen cycle")
	assert.False(t, u2Exists, "u2 should be removed because it was not in guild voice states")
}

// ===========================================================================
// 7. TestVoiceUpdate_RapidEvents
// ===========================================================================

func TestVoiceUpdate_RapidEvents(t *testing.T) {
	l, bs, mc := setupDiscordHandlerTest(nil)

	// Guild has 100 users
	states := make([]discord.VoiceState, 100)
	for i := 0; i < 100; i++ {
		states[i] = discord.VoiceState{
			UserID:    fmt.Sprintf("rapid-%d", i),
			ChannelID: bs.BridgeConfig.CID,
			GuildID:   bs.BridgeConfig.GID,
		}
	}
	mc.mu.Lock()
	mc.guild.VoiceStates = states
	mc.mu.Unlock()

	// Fire 100 rapid concurrent voice state events
	runConcurrentlyWithTimeout(t, 10*time.Second, 100, func(i int) {
		l.OnVoiceStateUpdate(&discord.VoiceState{
			UserID:    fmt.Sprintf("rapid-%d", i),
			ChannelID: bs.BridgeConfig.CID,
			GuildID:   bs.BridgeConfig.GID,
		})
	})

	// All 100 users should be tracked
	bs.DiscordUsersMutex.Lock()
	count := len(bs.DiscordUsers)
	bs.DiscordUsersMutex.Unlock()

	assert.Equal(t, 100, count, "all 100 users should be tracked after rapid events")
}

// ===========================================================================
// 8. TestDiscordListener_GuildCreateIgnoresWrongGuild
// ===========================================================================

func TestDiscordListener_GuildCreateIgnoresWrongGuild(t *testing.T) {
	l, bs, _ := setupDiscordHandlerTest(nil)

	// GuildCreate with a completely wrong guild -- should not panic
	require.NotPanics(t, func() {
		l.OnGuildCreate(&discord.Guild{
			ID:   "some-other-guild",
			Name: "Other",
			VoiceStates: []discord.VoiceState{
				{UserID: "u-other", ChannelID: bs.BridgeConfig.CID, GuildID: "some-other-guild"},
			},
		})
	})

	// No users should have been added
	bs.DiscordUsersMutex.Lock()
	count := len(bs.DiscordUsers)
	bs.DiscordUsersMutex.Unlock()

	assert.Equal(t, 0, count, "GuildCreate with wrong guild ID should be ignored")
}

// ===========================================================================
// 9. TestVoiceUpdate_UserRemovalTracking
// ===========================================================================

func TestVoiceUpdate_UserRemovalTracking(t *testing.T) {
	l, bs, mc := setupDiscordHandlerTest(nil)

	// Step 1: add 5 users
	states := make([]discord.VoiceState, 5)
	for i := 0; i < 5; i++ {
		states[i] = discord.VoiceState{
			UserID:    fmt.Sprintf("rem-%d", i),
			ChannelID: bs.BridgeConfig.CID,
			GuildID:   bs.BridgeConfig.GID,
		}
	}
	mc.mu.Lock()
	mc.guild.VoiceStates = states
	mc.mu.Unlock()

	l.OnVoiceStateUpdate(&discord.VoiceState{
		UserID:    "rem-0",
		ChannelID: bs.BridgeConfig.CID,
		GuildID:   bs.BridgeConfig.GID,
	})

	bs.DiscordUsersMutex.Lock()
	count1 := len(bs.DiscordUsers)
	bs.DiscordUsersMutex.Unlock()
	assert.Equal(t, 5, count1, "all 5 users should be present after initial sync")

	// Step 2: mark all seen=false by doing a second update with only 3 users
	mc.mu.Lock()
	mc.guild.VoiceStates = states[:3]
	mc.mu.Unlock()

	l.OnVoiceStateUpdate(&discord.VoiceState{
		UserID:    "rem-3",
		ChannelID: "",
		GuildID:   bs.BridgeConfig.GID,
	})

	bs.DiscordUsersMutex.Lock()
	count2 := len(bs.DiscordUsers)
	bs.DiscordUsersMutex.Unlock()
	assert.Equal(t, 3, count2, "only 3 users should remain after removal cycle")

	// Step 3: re-mark with all 3 still present, no further removals
	l.OnVoiceStateUpdate(&discord.VoiceState{
		UserID:    "rem-0",
		ChannelID: bs.BridgeConfig.CID,
		GuildID:   bs.BridgeConfig.GID,
	})

	bs.DiscordUsersMutex.Lock()
	count3 := len(bs.DiscordUsers)
	bs.DiscordUsersMutex.Unlock()
	assert.Equal(t, 3, count3, "count should stay at 3 after re-mark")
}

// ===========================================================================
// 10. TestVoiceUpdate_ConcurrentWithBridgeOps
// ===========================================================================

func TestVoiceUpdate_ConcurrentWithBridgeOps(t *testing.T) {
	l, bs, mc := setupDiscordHandlerTest(nil)

	// Guild has 30 users
	states := make([]discord.VoiceState, 30)
	for i := 0; i < 30; i++ {
		states[i] = discord.VoiceState{
			UserID:    fmt.Sprintf("conc-%d", i),
			ChannelID: bs.BridgeConfig.CID,
			GuildID:   bs.BridgeConfig.GID,
		}
	}
	mc.mu.Lock()
	mc.guild.VoiceStates = states
	mc.mu.Unlock()

	assertNoDeadlock(t, 10*time.Second, func() {
		var wg sync.WaitGroup

		// 30 VoiceStateUpdate calls
		for i := 0; i < 30; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				l.OnVoiceStateUpdate(&discord.VoiceState{
					UserID:    fmt.Sprintf("conc-%d", idx),
					ChannelID: bs.BridgeConfig.CID,
					GuildID:   bs.BridgeConfig.GID,
				})
			}(i)
		}

		// 30 bridge state toggles
		for i := 0; i < 30; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				bs.BridgeMutex.Lock()
				bs.Connected = idx%2 == 0
				bs.BridgeMutex.Unlock()
			}(i)
		}

		// 30 map reads
		for i := 0; i < 30; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				bs.DiscordUsersMutex.Lock()
				for k := range bs.DiscordUsers {
					_ = bs.DiscordUsers[k].username
				}
				bs.DiscordUsersMutex.Unlock()
			}()
		}

		wg.Wait()
	})
}

// ===========================================================================
// 11. TestMessageCreate_CommandFromWrongChannel
// ===========================================================================

func TestMessageCreate_CommandFromWrongChannel(t *testing.T) {
	l, bs, mc := setupDiscordHandlerTest(nil)

	// Enable discord commands
	bs.BridgeConfig.DiscordCommand = true

	l.OnMessageCreate(&discord.Message{
		ID:        "msg-1",
		ChannelID: "wrong-channel-id",
		GuildID:   bs.BridgeConfig.GID,
		Content:   "!" + bs.BridgeConfig.Command + " help",
		Author:    discord.User{ID: "some-user", Username: "TestUser"},
	})

	// No messages should have been sent (command ignored from wrong channel)
	msgs := mc.getSentMessages()
	assert.Empty(t, msgs, "command from wrong channel should be silently ignored")
}

// ===========================================================================
// 12. TestMessageCreate_CommandFromCorrectChannel
// ===========================================================================

func TestMessageCreate_CommandFromCorrectChannel(t *testing.T) {
	l, bs, mc := setupDiscordHandlerTest(nil)

	// Enable discord commands
	bs.BridgeConfig.DiscordCommand = true

	require.NotPanics(t, func() {
		l.OnMessageCreate(&discord.Message{
			ID:        "msg-2",
			ChannelID: bs.BridgeConfig.CID,
			GuildID:   bs.BridgeConfig.GID,
			Content:   "!" + bs.BridgeConfig.Command + " help",
			Author:    discord.User{ID: "some-user", Username: "TestUser"},
		})
	})

	// The help command should have triggered a SendMessage response
	msgs := mc.getSentMessages()
	require.NotEmpty(t, msgs, "help command from correct channel should produce a response")
	assert.Contains(t, msgs[0].content, "Commands:", "response should contain the help text")
	assert.Equal(t, bs.BridgeConfig.CID, msgs[0].channelID, "response should be sent to the configured channel")
}
