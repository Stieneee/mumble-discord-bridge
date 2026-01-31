package bridge

import (
	"fmt"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/stieneee/gumble/gumble"
)

// DiscordListener holds references to the current BridgeConf
// and BridgeState for use by the event handlers
type DiscordListener struct {
	Bridge *BridgeState
}

// GuildCreate handles Discord guild creation events.
func (l *DiscordListener) GuildCreate(s *discordgo.Session, event *discordgo.GuildCreate) {
	if event.ID != l.Bridge.BridgeConfig.GID {
		return
	}

	for _, vs := range event.VoiceStates {
		if vs.ChannelID == l.Bridge.DiscordChannelID {
			if s.State.User.ID == vs.UserID {
				// Ignore bot
				continue
			}

			u, err := s.User(vs.UserID)
			if err != nil {
				l.Bridge.Logger.Error("DISCORD_HANDLER", "Error looking up username")
			}

			dm, err := s.UserChannelCreate(u.ID)
			if err != nil {
				l.Bridge.Logger.Error("DISCORD_HANDLER", fmt.Sprintf("Error creating private channel for %s", u.Username))
			}

			l.Bridge.DiscordUsersMutex.Lock()
			l.Bridge.DiscordUsers[vs.UserID] = DiscordUser{
				username: u.Username,
				seen:     true,
				dm:       dm,
			}
			l.Bridge.DiscordUsersMutex.Unlock()

			// If connected to mumble inform users of Discord users
			l.Bridge.BridgeMutex.Lock()
			connected := l.Bridge.Connected
			disableText := l.Bridge.BridgeConfig.MumbleDisableText
			// Get current Mumble client from connection manager
			var mumbleClient *gumble.Client
			if l.Bridge.MumbleConnectionManager != nil {
				mumbleClient = l.Bridge.MumbleConnectionManager.GetClient()
			}
			l.Bridge.BridgeMutex.Unlock()

			if connected && !disableText && mumbleClient != nil {
				mumbleClient.Do(func() {
					if mumbleClient.Self != nil && mumbleClient.Self.Channel != nil {
						mumbleClient.Self.Channel.Send(fmt.Sprintf("%v has joined Discord\n", u.Username), false)
					}
				})
			}

			// Notify external systems about the user count change
			l.Bridge.notifyMetricsChange()
		}
	}
}

// MessageCreate handles Discord message creation events.
func (l *DiscordListener) MessageCreate(s *discordgo.Session, m *discordgo.MessageCreate) {
	l.Bridge.Logger.Debug("DISCORD_HANDLER", fmt.Sprintf("MessageCreate called from Discord user: %s", m.Author.Username))

	// Ignore all messages created by the bot itself
	if m.Author.ID == s.State.User.ID {
		l.Bridge.Logger.Debug("DISCORD_HANDLER", "Ignoring message from self")

		return
	}

	// Find the channel that the message came from.
	var guildID string

	// Try to get channel from state cache first
	c, err := s.State.Channel(m.ChannelID)
	if err != nil {
		l.Bridge.Logger.Debug("DISCORD_HANDLER", fmt.Sprintf("Channel not in state cache: %s - Error: %v", m.ChannelID, err))
		l.Bridge.Logger.Debug("DISCORD_HANDLER", fmt.Sprintf("Falling back to direct guild ID from message: %s", m.GuildID))

		// If we can't find the channel in state, use the guild ID from the message directly
		guildID = m.GuildID

		// Try to fetch the channel directly from the API if we need to (optional)
		apiChannel, err := s.Channel(m.ChannelID)
		if err != nil {
			l.Bridge.Logger.Debug("DISCORD_HANDLER", fmt.Sprintf("Could not fetch channel via API: %s - Error: %v", m.ChannelID, err))
			// Continue with the guild ID from the message
		} else {
			l.Bridge.Logger.Debug("DISCORD_HANDLER", fmt.Sprintf("Successfully fetched channel via API: %s in guild %s",
				apiChannel.ID, apiChannel.GuildID))
			c = apiChannel
		}
	} else {
		guildID = c.GuildID
		l.Bridge.Logger.Debug("DISCORD_HANDLER", fmt.Sprintf("Found channel %s in state cache", m.ChannelID))
	}

	// Find the guild for that channel
	var g *discordgo.Guild
	if c != nil && c.GuildID != "" {
		g, err = s.State.Guild(c.GuildID)
		if err != nil {
			l.Bridge.Logger.Debug("DISCORD_HANDLER", fmt.Sprintf("Could not find guild for channel: %s - Error: %v", c.ID, err))
			// Continue with guild ID from message
		}
	}

	// If we still don't have a guild, try to get it directly from the state using message's guild ID
	if g == nil && m.GuildID != "" {
		g, err = s.State.Guild(m.GuildID)
		if err != nil {
			l.Bridge.Logger.Debug("DISCORD_HANDLER", fmt.Sprintf("Could not find guild from message guild ID: %s - Error: %v",
				m.GuildID, err))
			// Continue without guild object
		}
	}

	// Check if we have a valid guild to work with
	if g == nil {
		l.Bridge.Logger.Debug("DISCORD_HANDLER", fmt.Sprintf("Guild object is nil, comparing message guild ID: %s with expected guild: %s",
			guildID, l.Bridge.BridgeConfig.GID))

		// Use the guild ID we extracted earlier
		if guildID != l.Bridge.BridgeConfig.GID {
			l.Bridge.Logger.Debug("DISCORD_HANDLER", "Guild ID mismatch using message guild ID, ignoring message")

			return
		}

		l.Bridge.Logger.Debug("DISCORD_HANDLER", "Guild ID matches configuration using message guild ID")
	} else {
		l.Bridge.Logger.Debug("DISCORD_HANDLER", fmt.Sprintf("Message from guild %s (%s), expected guild: %s",
			g.Name, g.ID, l.Bridge.BridgeConfig.GID))

		// the guild has to match the config
		// If we maintain a single bridge per bot and guild this provides sufficient protection
		// If a user wants multiple bridges in one guild they will need to define multiple bots
		if g.ID != l.Bridge.BridgeConfig.GID {
			l.Bridge.Logger.Debug("DISCORD_HANDLER", "Guild ID mismatch, ignoring message")

			return
		}

		l.Bridge.Logger.Debug("DISCORD_HANDLER", "Guild ID matches configuration")
	}

	prefix := "!" + l.Bridge.BridgeConfig.Command

	l.Bridge.BridgeMutex.Lock()
	bridgeConnected := l.Bridge.Connected
	l.Bridge.BridgeMutex.Unlock()

	// If the message starts with "!" then send it to HandleCommand else process as chat
	// the HandleCommand function is also used by the mumble listener
	if strings.HasPrefix(m.Content, "!") {
		// check if discord command is enabled
		if !l.Bridge.BridgeConfig.DiscordCommand {
			return
		}

		// Only process commands from the configured channel
		if m.ChannelID != l.Bridge.BridgeConfig.CID {
			l.Bridge.Logger.Debug("DISCORD_HANDLER",
				fmt.Sprintf("Ignoring command from channel %s (bound to %s)",
					m.ChannelID, l.Bridge.BridgeConfig.CID))

			return
		}

		// process the shared command options
		l.Bridge.HandleCommand(m.Content, func(s string) {
			_, err := l.Bridge.DiscordSession.ChannelMessageSend(m.ChannelID, s)
			if err != nil {
				l.Bridge.Logger.Error("DISCORD_HANDLER", fmt.Sprintf("Error sending command response: %v", err))
			}
		})

		// process the Discord specific command options

		if strings.HasPrefix(m.Content, prefix+" link") {
			if l.Bridge.Mode == BridgeModeConstant && strings.HasPrefix(m.Content, prefix) {
				_, err := l.Bridge.DiscordSession.ChannelMessageSend(m.ChannelID, "Constant mode enabled, link commands can not be entered")
				if err != nil {
					l.Bridge.Logger.Error("DISCORD_HANDLER", fmt.Sprintf("Error sending constant mode message: %v", err))
				}

				return
			}
			// Look for the message sender in that guild's current voice states.
			if bridgeConnected {
				_, err := l.Bridge.DiscordSession.ChannelMessageSend(m.ChannelID, "Bridge already running, unlink first")
				if err != nil {
					l.Bridge.Logger.Error("DISCORD_HANDLER", fmt.Sprintf("Error sending bridge status message: %v", err))
				}

				return
			}

			// If we have a guild object, check voice states
			if g != nil {
				// Look for the message sender in that guild's current voice states.
				for _, vs := range g.VoiceStates {
					if vs.UserID == m.Author.ID {
						// User must be in the configured voice channel
						if vs.ChannelID != l.Bridge.BridgeConfig.CID {
							_, err := l.Bridge.DiscordSession.ChannelMessageSend(m.ChannelID,
								"Please join the configured voice channel to use this bridge")
							if err != nil {
								l.Bridge.Logger.Error("DISCORD_HANDLER", fmt.Sprintf("Error sending channel mismatch message: %v", err))
							}

							return
						}

						l.Bridge.Logger.Info("DISCORD_HANDLER", fmt.Sprintf("Trying to join GID %v and VID %v", g.ID, vs.ChannelID))
						go l.Bridge.StartBridge()

						return
					}
				}
			} else {
				// We can't get voice states if we don't have a guild
				guild, err := s.Guild(guildID)
				if err != nil {
					l.Bridge.Logger.Error("DISCORD_HANDLER", fmt.Sprintf("Error fetching guild: %v", err))
					_, err := l.Bridge.DiscordSession.ChannelMessageSend(m.ChannelID,
						"Couldn't detect your voice channel. Please join a voice channel first.")
					if err != nil {
						l.Bridge.Logger.Error("DISCORD_HANDLER", fmt.Sprintf("Error sending voice channel message: %v", err))
					}

					return
				}

				// Process the guild's voice states
				foundUser := false
				for _, vs := range guild.VoiceStates {
					if vs.UserID == m.Author.ID {
						foundUser = true
						if vs.ChannelID != "" {
							// User must be in the configured voice channel
							if vs.ChannelID != l.Bridge.BridgeConfig.CID {
								_, err := l.Bridge.DiscordSession.ChannelMessageSend(m.ChannelID,
									"Please join the configured voice channel to use this bridge")
								if err != nil {
									l.Bridge.Logger.Error("DISCORD_HANDLER", fmt.Sprintf("Error sending channel mismatch message: %v", err))
								}

								return
							}

							l.Bridge.Logger.Info("DISCORD_HANDLER", fmt.Sprintf("Trying to join GID %v and VID %v", guildID, vs.ChannelID))
							go l.Bridge.StartBridge()

							return
						}
					}
				}

				// If we get here, user isn't in a voice channel
				message := "Couldn't find you in a voice channel. Please join a voice channel first."
				if foundUser {
					message = "Please join a voice channel first."
				}

				_, err = l.Bridge.DiscordSession.ChannelMessageSend(m.ChannelID, message)
				if err != nil {
					l.Bridge.Logger.Error("DISCORD_HANDLER", fmt.Sprintf("Error sending voice channel message: %v", err))
				}
			}
		}

		if strings.HasPrefix(m.Content, prefix+" unlink") {
			if l.Bridge.Mode == BridgeModeConstant && strings.HasPrefix(m.Content, prefix) {
				_, err := l.Bridge.DiscordSession.ChannelMessageSend(m.ChannelID, "Constant mode enabled, link commands can not be entered")
				if err != nil {
					l.Bridge.Logger.Error("DISCORD_HANDLER", fmt.Sprintf("Error sending constant mode message: %v", err))
				}

				return
			}
			// Look for the message sender in that guild's current voice states.
			if !bridgeConnected {
				_, err := l.Bridge.DiscordSession.ChannelMessageSend(m.ChannelID, "Bridge is not currently running")
				if err != nil {
					l.Bridge.Logger.Error("DISCORD_HANDLER", fmt.Sprintf("Error sending bridge status message: %v", err))
				}

				return
			}

			// Handle the case when guild might be nil
			if g == nil {
				l.Bridge.Logger.Info("DISCORD_HANDLER", fmt.Sprintf("Guild object is nil, allowing unlink for guild ID %v anyway", guildID))
				l.Bridge.BridgeDie <- true

				return
			}

			for _, vs := range g.VoiceStates {
				if vs.UserID == m.Author.ID && vs.ChannelID == l.Bridge.DiscordChannelID {
					l.Bridge.Logger.Info("DISCORD_HANDLER", fmt.Sprintf("Trying to leave GID %v and VID %v", g.ID, vs.ChannelID))
					l.Bridge.BridgeDie <- true

					return
				}
			}
		}

		if strings.HasPrefix(m.Content, prefix+" refresh") {
			if l.Bridge.Mode == BridgeModeConstant && strings.HasPrefix(m.Content, prefix) {
				_, err := l.Bridge.DiscordSession.ChannelMessageSend(m.ChannelID, "Constant mode enabled, link commands can not be entered")
				if err != nil {
					l.Bridge.Logger.Error("DISCORD_HANDLER", fmt.Sprintf("Error sending constant mode message: %v", err))
				}

				return
			}
			// Look for the message sender in that guild's current voice states.
			if !bridgeConnected {
				_, err := l.Bridge.DiscordSession.ChannelMessageSend(m.ChannelID, "Bridge is not currently running")
				if err != nil {
					l.Bridge.Logger.Error("DISCORD_HANDLER", fmt.Sprintf("Error sending bridge status message: %v", err))
				}

				return
			}

			// Handle the case when guild might be nil
			if g == nil {
				l.Bridge.Logger.Info("DISCORD_HANDLER", fmt.Sprintf("Guild object is nil, allowing refresh for guild ID %v anyway", guildID))
				l.Bridge.BridgeDie <- true
				time.Sleep(5 * time.Second)
				go l.Bridge.StartBridge()

				return
			}

			for _, vs := range g.VoiceStates {
				if vs.UserID == m.Author.ID {
					l.Bridge.Logger.Info("DISCORD_HANDLER", fmt.Sprintf("Trying to refresh GID %v and VID %v", g.ID, vs.ChannelID))
					l.Bridge.BridgeDie <- true

					time.Sleep(5 * time.Second)

					go l.Bridge.StartBridge()

					return
				}
			}
		}
	} else if !strings.HasPrefix(m.Content, "!") {
		// Get a truncated version of the message for logs
		content := m.Content
		if len(content) > 50 {
			content = content[:47] + "..."
		}

		// Check if chat bridge is enabled
		if !l.Bridge.BridgeConfig.ChatBridge {
			l.Bridge.Logger.Debug("DISCORD→MUMBLE", fmt.Sprintf("Chat message received but ChatBridge is DISABLED: %s", content))

			return
		}

		// Check if the bridge is connected
		l.Bridge.BridgeMutex.Lock()
		bridgeConnected := l.Bridge.Connected
		l.Bridge.BridgeMutex.Unlock()

		if !bridgeConnected {
			l.Bridge.Logger.Debug("DISCORD→MUMBLE", "Chat message received but bridge is not connected")

			return
		}

		// Check if text messages to Mumble are disabled
		if l.Bridge.BridgeConfig.MumbleDisableText {
			l.Bridge.Logger.Debug("DISCORD→MUMBLE", "Chat message received but MumbleDisableText is true")

			return
		}

		l.Bridge.Logger.Debug("DISCORD→MUMBLE", fmt.Sprintf("Forwarding message from %s", m.Author.Username))

		// Get MumbleClient reference under lock to prevent race conditions
		l.Bridge.BridgeMutex.Lock()
		// Get current Mumble client from connection manager
		var mumbleClient *gumble.Client
		if l.Bridge.MumbleConnectionManager != nil {
			mumbleClient = l.Bridge.MumbleConnectionManager.GetClient()
		}
		l.Bridge.BridgeMutex.Unlock()

		// Perform null checks
		if mumbleClient == nil ||
			mumbleClient.Self == nil ||
			mumbleClient.Self.Channel == nil {

			l.Bridge.Logger.Error("DISCORD→MUMBLE", "Cannot forward message - MumbleClient is not properly initialized")

			return
		}

		// Format and send the message to Mumble
		message := fmt.Sprintf("%v: %v\n", m.Author.Username, m.Content)

		// Use a separate goroutine with timeout to make the call more resilient
		messageSent := make(chan bool, 1)
		go func() {
			mumbleClient.Do(func() {
				mumbleClient.Self.Channel.Send(message, false)
				messageSent <- true
			})
		}()

		// Wait for confirmation or timeout
		select {
		case <-messageSent:
			l.Bridge.Logger.Debug("DISCORD→MUMBLE", "Successfully forwarded message")
		case <-time.After(2 * time.Second):
			l.Bridge.Logger.Error("DISCORD→MUMBLE", "Timed out while trying to send message")
		}
	}
}

// VoiceUpdate handles Discord voice state update events.
// Lock order: BridgeMutex -> MumbleUsersMutex -> DiscordUsersMutex
// This function is careful to avoid nested lock acquisition by collecting data
// under one lock, releasing it, then acquiring another lock.
func (l *DiscordListener) VoiceUpdate(s *discordgo.Session, event *discordgo.VoiceStateUpdate) {
	if event.GuildID != l.Bridge.BridgeConfig.GID {
		return
	}

	// Use State.RLock to safely read guild state
	s.State.RLock()
	g, err := s.State.Guild(l.Bridge.BridgeConfig.GID)
	if err != nil {
		s.State.RUnlock()
		// Don't panic, just return since we can't proceed
		return
	}

	// Make a defensive copy of VoiceStates to avoid race conditions
	var voiceStates []*discordgo.VoiceState
	if g.VoiceStates != nil {
		voiceStates = make([]*discordgo.VoiceState, len(g.VoiceStates))
		copy(voiceStates, g.VoiceStates)
	}
	s.State.RUnlock()

	// Collect users to add and users to remove under DiscordUsersMutex
	type userToAdd struct {
		userID   string
		username string
		dm       *discordgo.Channel
	}
	type userToRemove struct {
		userID   string
		username string
	}
	var usersToAdd []userToAdd
	var usersToRemove []userToRemove
	var userCount int

	l.Bridge.DiscordUsersMutex.Lock()

	// Mark all users as unseen
	for u := range l.Bridge.DiscordUsers {
		du := l.Bridge.DiscordUsers[u]
		du.seen = false
		l.Bridge.DiscordUsers[u] = du
	}

	// Sync the channel voice states to the local discordUsersMap
	for _, vs := range voiceStates {
		if vs.ChannelID == l.Bridge.DiscordChannelID {
			if s.State.User.ID == vs.UserID {
				// Ignore bot
				continue
			}

			if _, ok := l.Bridge.DiscordUsers[vs.UserID]; !ok {
				u, err := s.User(vs.UserID)
				if err != nil {
					l.Bridge.Logger.Error("DISCORD_HANDLER", "Error looking up username")

					continue
				}

				l.Bridge.Logger.Info("DISCORD_HANDLER", fmt.Sprintf("User joined Discord: %s", u.Username))

				// Emit user joined event
				l.Bridge.EmitUserEvent("discord", 0, u.Username, nil)
				dm, err := s.UserChannelCreate(u.ID)
				if err != nil {
					l.Bridge.Logger.Error("DISCORD_HANDLER", fmt.Sprintf("Error creating private channel for %s", u.Username))
				}
				l.Bridge.DiscordUsers[vs.UserID] = DiscordUser{
					username: u.Username,
					seen:     true,
					dm:       dm,
				}
				usersToAdd = append(usersToAdd, userToAdd{
					userID:   vs.UserID,
					username: u.Username,
					dm:       dm,
				})
			} else {
				du := l.Bridge.DiscordUsers[vs.UserID]
				du.seen = true
				l.Bridge.DiscordUsers[vs.UserID] = du
			}
		}
	}

	// Identify users that are no longer connected
	for id := range l.Bridge.DiscordUsers {
		if !l.Bridge.DiscordUsers[id].seen {
			username := l.Bridge.DiscordUsers[id].username
			l.Bridge.Logger.Info("DISCORD_HANDLER", fmt.Sprintf("User left Discord channel: %s", username))

			// Emit user left event
			l.Bridge.EmitUserEvent("discord", 1, username, nil)
			usersToRemove = append(usersToRemove, userToRemove{
				userID:   id,
				username: username,
			})
		}
	}

	// Remove users from the map while still holding the lock
	for _, u := range usersToRemove {
		delete(l.Bridge.DiscordUsers, u.userID)
	}

	userCount = len(l.Bridge.DiscordUsers)
	l.Bridge.DiscordUsersMutex.Unlock()

	// Now acquire BridgeMutex to read bridge state and send Mumble messages
	// This avoids nested lock acquisition (DiscordUsersMutex is released above)
	l.Bridge.BridgeMutex.Lock()
	connected := l.Bridge.Connected
	disableText := l.Bridge.BridgeConfig.MumbleDisableText
	var mumbleClient *gumble.Client
	if l.Bridge.MumbleConnectionManager != nil {
		mumbleClient = l.Bridge.MumbleConnectionManager.GetClient()
	}
	l.Bridge.BridgeMutex.Unlock()

	// Send Mumble messages for users that joined
	if connected && !disableText && mumbleClient != nil {
		for _, u := range usersToAdd {
			username := u.username // Capture for closure
			mumbleClient.Do(func() {
				if mumbleClient.Self != nil && mumbleClient.Self.Channel != nil {
					mumbleClient.Self.Channel.Send(fmt.Sprintf("%v has joined Discord\n", username), false)
				}
			})
		}

		// Send Mumble messages for users that left
		for _, u := range usersToRemove {
			username := u.username // Capture for closure
			mumbleClient.Do(func() {
				if mumbleClient.Self != nil && mumbleClient.Self.Channel != nil {
					mumbleClient.Self.Channel.Send(fmt.Sprintf("%v has left Discord channel\n", username), false)
				}
			})
		}
	}

	// Update metrics
	promDiscordUsers.Set(float64(userCount))
}
