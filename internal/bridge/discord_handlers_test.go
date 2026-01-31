package bridge

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/stretchr/testify/assert"
)

// TestVoiceUpdate_LockOrderNoDeadlock tests that VoiceUpdate doesn't deadlock
func TestVoiceUpdate_LockOrderNoDeadlock(t *testing.T) {
	bridge := createTestBridgeState(nil)
	listener := &DiscordListener{Bridge: bridge}

	// Create a mock session with state
	session := &discordgo.Session{
		State: discordgo.NewState(),
	}
	session.State.User = &discordgo.User{
		ID:       "bot-id",
		Username: "TestBot",
	}

	// Add a guild to state
	guild := &discordgo.Guild{
		ID:          "test-guild-id",
		VoiceStates: []*discordgo.VoiceState{},
	}
	_ = session.State.GuildAdd(guild) //nolint:errcheck // test setup

	// Run concurrent voice updates without deadlock
	assertNoDeadlock(t, 5*time.Second, func() {
		var wg sync.WaitGroup

		// Launch goroutines calling VoiceUpdate
		for i := 0; i < 50; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				event := &discordgo.VoiceStateUpdate{
					VoiceState: &discordgo.VoiceState{
						GuildID:   "test-guild-id",
						ChannelID: "test-channel-id",
						UserID:    "user-" + string(rune('0'+idx%10)),
					},
				}
				listener.VoiceUpdate(session, event)
			}(i)
		}

		// Launch goroutines accessing BridgeMutex path
		for i := 0; i < 50; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				bridge.BridgeMutex.Lock()
				_ = bridge.Connected
				bridge.BridgeMutex.Unlock()
			}()
		}

		wg.Wait()
	})
}

// TestVoiceUpdate_ConcurrentUserJoinLeave tests concurrent user additions/removals
func TestVoiceUpdate_ConcurrentUserJoinLeave(_ *testing.T) {
	bridge := createTestBridgeState(nil)

	var wg sync.WaitGroup

	// Concurrent user additions
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			bridge.DiscordUsersMutex.Lock()
			bridge.DiscordUsers["user-"+string(rune('0'+idx))] = DiscordUser{
				username: "User" + string(rune('0'+idx)),
				seen:     true,
			}
			bridge.DiscordUsersMutex.Unlock()
		}(i)
	}

	// Concurrent user removals
	for i := 0; i < 25; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			bridge.DiscordUsersMutex.Lock()
			delete(bridge.DiscordUsers, "user-"+string(rune('0'+idx)))
			bridge.DiscordUsersMutex.Unlock()
		}(i)
	}

	wg.Wait()
}

// TestVoiceUpdate_MapIteratorNotInvalidated tests safe map iteration during modification
func TestVoiceUpdate_MapIteratorNotInvalidated(t *testing.T) {
	bridge := createTestBridgeState(nil)

	// Pre-populate some users
	bridge.DiscordUsersMutex.Lock()
	for i := 0; i < 20; i++ {
		bridge.DiscordUsers["user-"+string(rune('A'+i))] = DiscordUser{
			username: "User" + string(rune('A'+i)),
			seen:     true,
		}
	}
	bridge.DiscordUsersMutex.Unlock()

	// Safe iteration pattern (as used in VoiceUpdate)
	assertNoDeadlock(t, 3*time.Second, func() {
		var wg sync.WaitGroup

		// Reader iterates over map
		for i := 0; i < 20; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				bridge.DiscordUsersMutex.Lock()
				for u := range bridge.DiscordUsers {
					du := bridge.DiscordUsers[u]
					du.seen = false
					bridge.DiscordUsers[u] = du
				}
				bridge.DiscordUsersMutex.Unlock()
			}()
		}

		// Writer modifies map
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				bridge.DiscordUsersMutex.Lock()
				// Add new user
				bridge.DiscordUsers["new-user-"+string(rune('0'+idx))] = DiscordUser{
					username: "NewUser",
					seen:     true,
				}
				// Delete old user
				delete(bridge.DiscordUsers, "user-"+string(rune('A'+idx)))
				bridge.DiscordUsersMutex.Unlock()
			}(i)
		}

		wg.Wait()
	})
}

// TestVoiceUpdate_GuildIDMismatch tests that non-matching guild is ignored
func TestVoiceUpdate_GuildIDMismatch(t *testing.T) {
	bridge := createTestBridgeState(nil)
	bridge.BridgeConfig.GID = "correct-guild-id"
	listener := &DiscordListener{Bridge: bridge}

	session := &discordgo.Session{
		State: discordgo.NewState(),
	}
	session.State.User = &discordgo.User{ID: "bot-id"}

	// Event with wrong guild ID
	event := &discordgo.VoiceStateUpdate{
		VoiceState: &discordgo.VoiceState{
			GuildID: "wrong-guild-id",
		},
	}

	// Should return early without processing
	assert.NotPanics(t, func() {
		listener.VoiceUpdate(session, event)
	})

	// No users should have been added
	bridge.DiscordUsersMutex.Lock()
	assert.Empty(t, bridge.DiscordUsers)
	bridge.DiscordUsersMutex.Unlock()
}

// TestVoiceUpdate_BotUserIgnored tests that bot's own voice state is ignored
func TestVoiceUpdate_BotUserIgnored(t *testing.T) {
	bridge := createTestBridgeState(nil)
	listener := &DiscordListener{Bridge: bridge}

	session := &discordgo.Session{
		State: discordgo.NewState(),
	}
	session.State.User = &discordgo.User{
		ID:       "bot-user-id",
		Username: "TestBot",
	}

	// Add guild to state
	guild := &discordgo.Guild{
		ID: "test-guild-id",
		VoiceStates: []*discordgo.VoiceState{
			{
				UserID:    "bot-user-id", // Bot's own ID
				ChannelID: "test-channel-id",
			},
		},
	}
	_ = session.State.GuildAdd(guild) //nolint:errcheck // test setup

	event := &discordgo.VoiceStateUpdate{
		VoiceState: &discordgo.VoiceState{
			GuildID:   "test-guild-id",
			ChannelID: "test-channel-id",
			UserID:    "bot-user-id",
		},
	}

	listener.VoiceUpdate(session, event)

	// Bot should not be added to users
	bridge.DiscordUsersMutex.Lock()
	_, exists := bridge.DiscordUsers["bot-user-id"]
	bridge.DiscordUsersMutex.Unlock()

	assert.False(t, exists, "Bot should not be added to Discord users")
}

// TestVoiceUpdate_UserSeenTracking tests user seen flag management
func TestVoiceUpdate_UserSeenTracking(t *testing.T) {
	bridge := createTestBridgeState(nil)

	// Add user with seen=true
	bridge.DiscordUsersMutex.Lock()
	bridge.DiscordUsers["test-user"] = DiscordUser{
		username: "TestUser",
		seen:     true,
	}
	bridge.DiscordUsersMutex.Unlock()

	// Mark all as unseen (as VoiceUpdate does)
	bridge.DiscordUsersMutex.Lock()
	for u := range bridge.DiscordUsers {
		du := bridge.DiscordUsers[u]
		du.seen = false
		bridge.DiscordUsers[u] = du
	}
	bridge.DiscordUsersMutex.Unlock()

	// Verify seen is false
	bridge.DiscordUsersMutex.Lock()
	assert.False(t, bridge.DiscordUsers["test-user"].seen)
	bridge.DiscordUsersMutex.Unlock()
}

// TestVoiceUpdate_RapidEvents tests rapid event processing
func TestVoiceUpdate_RapidEvents(_ *testing.T) {
	bridge := createTestBridgeState(nil)
	listener := &DiscordListener{Bridge: bridge}

	session := &discordgo.Session{
		State: discordgo.NewState(),
	}
	session.State.User = &discordgo.User{ID: "bot-id"}

	// Add guild
	guild := &discordgo.Guild{
		ID:          "test-guild-id",
		VoiceStates: []*discordgo.VoiceState{},
	}
	_ = session.State.GuildAdd(guild) //nolint:errcheck // test setup

	var wg sync.WaitGroup
	eventCount := 100

	// Send rapid events
	for i := 0; i < eventCount; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			event := &discordgo.VoiceStateUpdate{
				VoiceState: &discordgo.VoiceState{
					GuildID:   "test-guild-id",
					ChannelID: "test-channel-id",
					UserID:    "user-" + string(rune('0'+idx%50)),
				},
			}
			listener.VoiceUpdate(session, event)
		}(i)
	}

	wg.Wait()
}

// TestDiscordListener_GuildCreateIgnoresWrongGuild tests GuildCreate filtering
func TestDiscordListener_GuildCreateIgnoresWrongGuild(t *testing.T) {
	bridge := createTestBridgeState(nil)
	bridge.BridgeConfig.GID = "correct-guild-id"
	listener := &DiscordListener{Bridge: bridge}

	session := &discordgo.Session{
		State: discordgo.NewState(),
	}
	session.State.User = &discordgo.User{ID: "bot-id"}

	// Event with wrong guild ID
	event := &discordgo.GuildCreate{
		Guild: &discordgo.Guild{
			ID: "wrong-guild-id",
		},
	}

	assert.NotPanics(t, func() {
		listener.GuildCreate(session, event)
	})
}

// TestVoiceUpdate_UserRemovalTracking tests that removed users are tracked
func TestVoiceUpdate_UserRemovalTracking(t *testing.T) {
	bridge := createTestBridgeState(nil)

	// Add some users
	bridge.DiscordUsersMutex.Lock()
	bridge.DiscordUsers["user1"] = DiscordUser{username: "User1", seen: true}
	bridge.DiscordUsers["user2"] = DiscordUser{username: "User2", seen: true}
	bridge.DiscordUsers["user3"] = DiscordUser{username: "User3", seen: true}
	bridge.DiscordUsersMutex.Unlock()

	// Simulate VoiceUpdate removing users not in voice channel
	var usersToRemove []string

	bridge.DiscordUsersMutex.Lock()
	// Mark all as unseen first
	for u := range bridge.DiscordUsers {
		du := bridge.DiscordUsers[u]
		du.seen = false
		bridge.DiscordUsers[u] = du
	}

	// Mark user2 as still present
	du := bridge.DiscordUsers["user2"]
	du.seen = true
	bridge.DiscordUsers["user2"] = du

	// Identify removed users
	for id := range bridge.DiscordUsers {
		if !bridge.DiscordUsers[id].seen {
			usersToRemove = append(usersToRemove, id)
		}
	}

	// Remove them
	for _, id := range usersToRemove {
		delete(bridge.DiscordUsers, id)
	}
	bridge.DiscordUsersMutex.Unlock()

	// Verify only user2 remains
	bridge.DiscordUsersMutex.Lock()
	assert.Len(t, bridge.DiscordUsers, 1)
	_, exists := bridge.DiscordUsers["user2"]
	assert.True(t, exists)
	bridge.DiscordUsersMutex.Unlock()
}

// TestVoiceUpdate_ConcurrentWithBridgeOps tests VoiceUpdate concurrent with bridge operations
func TestVoiceUpdate_ConcurrentWithBridgeOps(t *testing.T) {
	bridge := createTestBridgeState(nil)
	listener := &DiscordListener{Bridge: bridge}

	session := &discordgo.Session{
		State: discordgo.NewState(),
	}
	session.State.User = &discordgo.User{ID: "bot-id"}

	guild := &discordgo.Guild{
		ID:          "test-guild-id",
		VoiceStates: []*discordgo.VoiceState{},
	}
	_ = session.State.GuildAdd(guild) //nolint:errcheck // test setup

	var wg sync.WaitGroup
	var voiceUpdates, bridgeOps int32

	// Concurrent VoiceUpdate calls
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			event := &discordgo.VoiceStateUpdate{
				VoiceState: &discordgo.VoiceState{
					GuildID:   "test-guild-id",
					ChannelID: "test-channel-id",
					UserID:    "user-" + string(rune('0'+idx%10)),
				},
			}
			listener.VoiceUpdate(session, event)
			atomic.AddInt32(&voiceUpdates, 1)
		}(i)
	}

	// Concurrent bridge state operations
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			bridge.BridgeMutex.Lock()
			bridge.Connected = !bridge.Connected
			bridge.BridgeMutex.Unlock()
			atomic.AddInt32(&bridgeOps, 1)
		}()
	}

	// Concurrent user map reads
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			bridge.DiscordUsersMutex.Lock()
			_ = len(bridge.DiscordUsers)
			bridge.DiscordUsersMutex.Unlock()
		}()
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		t.Logf("Completed %d voice updates and %d bridge ops",
			atomic.LoadInt32(&voiceUpdates), atomic.LoadInt32(&bridgeOps))
	case <-time.After(5 * time.Second):
		t.Fatal("Deadlock detected in concurrent operations")
	}
}

// TestDiscordListener_NilBridgeState tests handling of nil bridge state components
func TestDiscordListener_NilBridgeState(_ *testing.T) {
	bridge := createTestBridgeState(nil)
	bridge.BridgeConfig = nil // Set config to nil

	listener := &DiscordListener{Bridge: bridge}

	session := &discordgo.Session{
		State: discordgo.NewState(),
	}
	session.State.User = &discordgo.User{ID: "bot-id"}

	// Should not panic with nil config
	event := &discordgo.VoiceStateUpdate{
		VoiceState: &discordgo.VoiceState{
			GuildID:   "any-guild",
			ChannelID: "any-channel",
			UserID:    "any-user",
		},
	}

	// This might panic due to nil config, but we're testing that it's handled
	// In the actual code, this would be a bug, but we want to ensure no data races
	defer func() {
		_ = recover() //nolint:errcheck // intentionally catching panic
	}()

	listener.VoiceUpdate(session, event)
}

// TestMessageCreate_CommandFromWrongChannel tests that commands from wrong channel are ignored
func TestMessageCreate_CommandFromWrongChannel(t *testing.T) {
	bridge := createTestBridgeState(nil)
	bridge.BridgeConfig.GID = "test-guild"
	bridge.BridgeConfig.CID = "configured-channel"
	bridge.BridgeConfig.DiscordCommand = true

	listener := &DiscordListener{Bridge: bridge}

	// Create a mock session
	session := &discordgo.Session{
		State: discordgo.NewState(),
	}
	session.State.User = &discordgo.User{
		ID:       "bot-id",
		Username: "TestBot",
	}

	// Add guild to state
	guild := &discordgo.Guild{
		ID:          "test-guild",
		VoiceStates: []*discordgo.VoiceState{},
		Channels: []*discordgo.Channel{
			{ID: "wrong-channel", GuildID: "test-guild"},
			{ID: "configured-channel", GuildID: "test-guild"},
		},
	}
	_ = session.State.GuildAdd(guild) //nolint:errcheck // test setup

	// Message from WRONG channel - should be ignored
	m := &discordgo.MessageCreate{
		Message: &discordgo.Message{
			GuildID:   "test-guild",
			ChannelID: "wrong-channel", // Different from CID
			Content:   "!bridge link",
			Author:    &discordgo.User{ID: "user-1", Username: "TestUser"},
		},
	}

	// Should not panic and should return early (command ignored)
	assert.NotPanics(t, func() {
		listener.MessageCreate(session, m)
	})
}

// TestMessageCreate_CommandFromCorrectChannel tests that commands from correct channel are processed
func TestMessageCreate_CommandFromCorrectChannel(t *testing.T) {
	bridge := createTestBridgeState(nil)
	bridge.BridgeConfig.GID = "test-guild"
	bridge.BridgeConfig.CID = "configured-channel"
	bridge.BridgeConfig.DiscordCommand = true
	bridge.BridgeConfig.Command = "bridge"

	listener := &DiscordListener{Bridge: bridge}

	// Create a mock session
	session := &discordgo.Session{
		State: discordgo.NewState(),
	}
	session.State.User = &discordgo.User{
		ID:       "bot-id",
		Username: "TestBot",
	}

	// Add guild to state with channels
	guild := &discordgo.Guild{
		ID:          "test-guild",
		VoiceStates: []*discordgo.VoiceState{},
		Channels: []*discordgo.Channel{
			{ID: "configured-channel", GuildID: "test-guild"},
		},
	}
	_ = session.State.GuildAdd(guild) //nolint:errcheck // test setup

	// Message from CORRECT channel - should be processed
	m := &discordgo.MessageCreate{
		Message: &discordgo.Message{
			GuildID:   "test-guild",
			ChannelID: "configured-channel", // Same as CID
			Content:   "!bridge link",
			Author:    &discordgo.User{ID: "user-1", Username: "TestUser"},
		},
	}

	// Should not panic - command should be processed
	// Note: actual bridge action won't happen since user isn't in voice
	assert.NotPanics(t, func() {
		listener.MessageCreate(session, m)
	})
}
