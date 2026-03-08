package bridge

import (
	"fmt"
	"strings"
	"time"

	"github.com/stieneee/gumble/gumble"
	"github.com/stieneee/mumble-discord-bridge/internal/discord"
)

// DiscordListener holds references to the current BridgeConf
// and BridgeState for use by the event handlers.
// It implements discord.EventHandler.
type DiscordListener struct {
	Bridge *BridgeState
}

// OnReady handles the Discord ready event.
func (l *DiscordListener) OnReady() {
	l.Bridge.Logger.Info("DISCORD_HANDLER", "Discord client is ready")
}

// OnGuildCreate handles Discord guild creation/availability events.
func (l *DiscordListener) OnGuildCreate(guild *discord.Guild) {
	if guild.ID != l.Bridge.BridgeConfig.GID {
		return
	}

	botID := l.Bridge.DiscordClient.GetBotUserID()

	for _, vs := range guild.VoiceStates {
		if vs.ChannelID == l.Bridge.DiscordChannelID {
			if botID == vs.UserID {
				// Ignore bot
				continue
			}

			u, err := l.Bridge.DiscordClient.GetUser(vs.UserID)
			if err != nil {
				l.Bridge.Logger.Error("DISCORD_HANDLER", "Error looking up username")

				continue
			}

			dmID, err := l.Bridge.DiscordClient.CreateDM(u.ID)
			if err != nil {
				l.Bridge.Logger.Error("DISCORD_HANDLER", fmt.Sprintf("Error creating private channel for %s", u.Username))
			}

			l.Bridge.DiscordUsersMutex.Lock()
			l.Bridge.DiscordUsers[vs.UserID] = DiscordUser{
				username: u.Username,
				seen:     true,
				dmID:     dmID,
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
				username := u.Username
				mumbleClient.Do(func() {
					if mumbleClient.Self != nil && mumbleClient.Self.Channel != nil {
						mumbleClient.Self.Channel.Send(fmt.Sprintf("%v has joined\n", username), false)
					}
				})
			}

			// Notify external systems about the user count change
			l.Bridge.notifyMetricsChange()
		}
	}
}

// OnMessageCreate handles Discord message creation events.
func (l *DiscordListener) OnMessageCreate(m *discord.Message) {
	l.Bridge.Logger.Debug("DISCORD_HANDLER", fmt.Sprintf("MessageCreate called from Discord user: %s", m.Author.Username))

	botID := l.Bridge.DiscordClient.GetBotUserID()

	// Ignore all messages created by the bot itself
	if m.Author.ID == botID {
		l.Bridge.Logger.Debug("DISCORD_HANDLER", "Ignoring message from self")

		return
	}

	// Check guild matches config
	if m.GuildID != l.Bridge.BridgeConfig.GID {
		l.Bridge.Logger.Debug("DISCORD_HANDLER", "Guild ID mismatch, ignoring message")

		return
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
			err := l.Bridge.DiscordClient.SendMessage(m.ChannelID, s)
			if err != nil {
				l.Bridge.Logger.Error("DISCORD_HANDLER", fmt.Sprintf("Error sending command response: %v", err))
			}
		})

		// process the Discord specific command options

		if strings.HasPrefix(m.Content, prefix+" link") {
			if l.Bridge.Mode == BridgeModeConstant && strings.HasPrefix(m.Content, prefix) {
				err := l.Bridge.DiscordClient.SendMessage(m.ChannelID, "Constant mode enabled, link commands can not be entered")
				if err != nil {
					l.Bridge.Logger.Error("DISCORD_HANDLER", fmt.Sprintf("Error sending constant mode message: %v", err))
				}

				return
			}
			// Look for the message sender in that guild's current voice states.
			if bridgeConnected {
				err := l.Bridge.DiscordClient.SendMessage(m.ChannelID, "Bridge already running, unlink first")
				if err != nil {
					l.Bridge.Logger.Error("DISCORD_HANDLER", fmt.Sprintf("Error sending bridge status message: %v", err))
				}

				return
			}

			// Get guild to check voice states
			guild, err := l.Bridge.DiscordClient.GetGuild(m.GuildID)
			if err != nil {
				l.Bridge.Logger.Error("DISCORD_HANDLER", fmt.Sprintf("Error fetching guild: %v", err))
				err := l.Bridge.DiscordClient.SendMessage(m.ChannelID,
					"Couldn't detect your voice channel. Please join a voice channel first.")
				if err != nil {
					l.Bridge.Logger.Error("DISCORD_HANDLER", fmt.Sprintf("Error sending voice channel message: %v", err))
				}

				return
			}

			for _, vs := range guild.VoiceStates {
				if vs.UserID == m.Author.ID {
					if vs.ChannelID != l.Bridge.BridgeConfig.CID {
						err := l.Bridge.DiscordClient.SendMessage(m.ChannelID,
							"Please join the configured voice channel to use this bridge")
						if err != nil {
							l.Bridge.Logger.Error("DISCORD_HANDLER", fmt.Sprintf("Error sending channel mismatch message: %v", err))
						}

						return
					}

					l.Bridge.Logger.Info("DISCORD_HANDLER", fmt.Sprintf("Trying to join GID %v and VID %v", guild.ID, vs.ChannelID))
					go l.Bridge.StartBridge()

					return
				}
			}

			err = l.Bridge.DiscordClient.SendMessage(m.ChannelID,
				"Couldn't find you in a voice channel. Please join a voice channel first.")
			if err != nil {
				l.Bridge.Logger.Error("DISCORD_HANDLER", fmt.Sprintf("Error sending voice channel message: %v", err))
			}
		}

		if strings.HasPrefix(m.Content, prefix+" unlink") {
			if l.Bridge.Mode == BridgeModeConstant && strings.HasPrefix(m.Content, prefix) {
				err := l.Bridge.DiscordClient.SendMessage(m.ChannelID, "Constant mode enabled, link commands can not be entered")
				if err != nil {
					l.Bridge.Logger.Error("DISCORD_HANDLER", fmt.Sprintf("Error sending constant mode message: %v", err))
				}

				return
			}
			// Look for the message sender in that guild's current voice states.
			if !bridgeConnected {
				err := l.Bridge.DiscordClient.SendMessage(m.ChannelID, "Bridge is not currently running")
				if err != nil {
					l.Bridge.Logger.Error("DISCORD_HANDLER", fmt.Sprintf("Error sending bridge status message: %v", err))
				}

				return
			}

			guild, err := l.Bridge.DiscordClient.GetGuild(m.GuildID)
			if err != nil {
				l.Bridge.Logger.Info("DISCORD_HANDLER", "Could not get guild, allowing unlink anyway")
				l.Bridge.BridgeDie <- true

				return
			}

			for _, vs := range guild.VoiceStates {
				if vs.UserID == m.Author.ID && vs.ChannelID == l.Bridge.DiscordChannelID {
					l.Bridge.Logger.Info("DISCORD_HANDLER", fmt.Sprintf("Trying to leave GID %v and VID %v", guild.ID, vs.ChannelID))
					l.Bridge.BridgeDie <- true

					return
				}
			}
		}

		if strings.HasPrefix(m.Content, prefix+" refresh") {
			if l.Bridge.Mode == BridgeModeConstant && strings.HasPrefix(m.Content, prefix) {
				err := l.Bridge.DiscordClient.SendMessage(m.ChannelID, "Constant mode enabled, link commands can not be entered")
				if err != nil {
					l.Bridge.Logger.Error("DISCORD_HANDLER", fmt.Sprintf("Error sending constant mode message: %v", err))
				}

				return
			}
			// Look for the message sender in that guild's current voice states.
			if !bridgeConnected {
				err := l.Bridge.DiscordClient.SendMessage(m.ChannelID, "Bridge is not currently running")
				if err != nil {
					l.Bridge.Logger.Error("DISCORD_HANDLER", fmt.Sprintf("Error sending bridge status message: %v", err))
				}

				return
			}

			guild, err := l.Bridge.DiscordClient.GetGuild(m.GuildID)
			if err != nil {
				l.Bridge.Logger.Info("DISCORD_HANDLER", "Could not get guild, allowing refresh anyway")
				l.Bridge.BridgeDie <- true
				time.Sleep(5 * time.Second)
				go l.Bridge.StartBridge()

				return
			}

			for _, vs := range guild.VoiceStates {
				if vs.UserID == m.Author.ID {
					l.Bridge.Logger.Info("DISCORD_HANDLER", fmt.Sprintf("Trying to refresh GID %v and VID %v", guild.ID, vs.ChannelID))
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

// OnVoiceStateUpdate handles Discord voice state update events.
// Lock order: BridgeMutex -> MumbleUsersMutex -> DiscordUsersMutex
// This function is careful to avoid nested lock acquisition by collecting data
// under one lock, releasing it, then acquiring another lock.
func (l *DiscordListener) OnVoiceStateUpdate(state *discord.VoiceState) {
	if state.GuildID != l.Bridge.BridgeConfig.GID {
		return
	}

	botID := l.Bridge.DiscordClient.GetBotUserID()

	if botID == state.UserID {
		return // Ignore bot's own voice state changes
	}

	// Try full guild reconciliation first
	guild, err := l.Bridge.DiscordClient.GetGuild(l.Bridge.BridgeConfig.GID)
	useGuildReconciliation := err == nil && len(guild.VoiceStates) > 0

	type userToAdd struct {
		userID   string
		username string
		dmID     string
	}
	type userToRemove struct {
		userID   string
		username string
	}
	var usersToAdd []userToAdd
	var usersToRemove []userToRemove
	var userCount int

	l.Bridge.DiscordUsersMutex.Lock()

	if useGuildReconciliation {
		// Full reconciliation against guild voice state cache
		// Mark all users as unseen
		for u := range l.Bridge.DiscordUsers {
			du := l.Bridge.DiscordUsers[u]
			du.seen = false
			l.Bridge.DiscordUsers[u] = du
		}

		for _, vs := range guild.VoiceStates {
			if vs.ChannelID == l.Bridge.DiscordChannelID {
				if botID == vs.UserID {
					continue
				}

				if _, ok := l.Bridge.DiscordUsers[vs.UserID]; !ok {
					u, err := l.Bridge.DiscordClient.GetUser(vs.UserID)
					if err != nil {
						l.Bridge.Logger.Error("DISCORD_HANDLER", "Error looking up username")

						continue
					}

					l.Bridge.Logger.Info("DISCORD_HANDLER", fmt.Sprintf("User joined Discord: %s", u.Username))
					l.Bridge.EmitUserEvent("discord", 0, u.Username, nil)

					dmID, err := l.Bridge.DiscordClient.CreateDM(u.ID)
					if err != nil {
						l.Bridge.Logger.Error("DISCORD_HANDLER", fmt.Sprintf("Error creating private channel for %s", u.Username))
					}
					l.Bridge.DiscordUsers[vs.UserID] = DiscordUser{
						username: u.Username,
						seen:     true,
						dmID:     dmID,
					}
					usersToAdd = append(usersToAdd, userToAdd{
						userID:   vs.UserID,
						username: u.Username,
						dmID:     dmID,
					})
				} else {
					du := l.Bridge.DiscordUsers[vs.UserID]
					du.seen = true
					l.Bridge.DiscordUsers[vs.UserID] = du
				}
			}
		}

		// Remove users no longer in channel
		for id := range l.Bridge.DiscordUsers {
			if !l.Bridge.DiscordUsers[id].seen {
				username := l.Bridge.DiscordUsers[id].username
				l.Bridge.Logger.Info("DISCORD_HANDLER", fmt.Sprintf("User left Discord channel: %s", username))
				l.Bridge.EmitUserEvent("discord", 1, username, nil)
				usersToRemove = append(usersToRemove, userToRemove{userID: id, username: username})
			}
		}
		for _, u := range usersToRemove {
			delete(l.Bridge.DiscordUsers, u.userID)
		}
	} else {
		// Fallback: guild cache has no voice states, process event directly
		if state.ChannelID == l.Bridge.DiscordChannelID {
			// User joined or is in our channel
			if _, ok := l.Bridge.DiscordUsers[state.UserID]; !ok {
				u, err := l.Bridge.DiscordClient.GetUser(state.UserID)
				if err != nil {
					l.Bridge.Logger.Error("DISCORD_HANDLER", "Error looking up username")
				} else {
					l.Bridge.Logger.Info("DISCORD_HANDLER", fmt.Sprintf("User joined Discord: %s", u.Username))
					l.Bridge.EmitUserEvent("discord", 0, u.Username, nil)

					dmID, err := l.Bridge.DiscordClient.CreateDM(u.ID)
					if err != nil {
						l.Bridge.Logger.Error("DISCORD_HANDLER", fmt.Sprintf("Error creating private channel for %s", u.Username))
					}
					l.Bridge.DiscordUsers[state.UserID] = DiscordUser{
						username: u.Username,
						seen:     true,
						dmID:     dmID,
					}
					usersToAdd = append(usersToAdd, userToAdd{
						userID:   state.UserID,
						username: u.Username,
						dmID:     dmID,
					})
				}
			}
		} else {
			// User left our channel (moved elsewhere or disconnected)
			if du, ok := l.Bridge.DiscordUsers[state.UserID]; ok {
				l.Bridge.Logger.Info("DISCORD_HANDLER", fmt.Sprintf("User left Discord channel: %s", du.username))
				l.Bridge.EmitUserEvent("discord", 1, du.username, nil)
				usersToRemove = append(usersToRemove, userToRemove{userID: state.UserID, username: du.username})
				delete(l.Bridge.DiscordUsers, state.UserID)
			}
		}
	}

	userCount = len(l.Bridge.DiscordUsers)
	l.Bridge.DiscordUsersMutex.Unlock()

	// Now acquire BridgeMutex to read bridge state and send Mumble messages
	l.Bridge.BridgeMutex.Lock()
	connected := l.Bridge.Connected
	disableText := l.Bridge.BridgeConfig.MumbleDisableText
	var mumbleClient *gumble.Client
	if l.Bridge.MumbleConnectionManager != nil {
		mumbleClient = l.Bridge.MumbleConnectionManager.GetClient()
	}
	l.Bridge.BridgeMutex.Unlock()

	if connected && !disableText && mumbleClient != nil {
		for _, u := range usersToAdd {
			username := u.username
			mumbleClient.Do(func() {
				if mumbleClient.Self != nil && mumbleClient.Self.Channel != nil {
					mumbleClient.Self.Channel.Send(fmt.Sprintf("%v has joined\n", username), false)
				}
			})
		}

		for _, u := range usersToRemove {
			username := u.username
			mumbleClient.Do(func() {
				if mumbleClient.Self != nil && mumbleClient.Self.Channel != nil {
					mumbleClient.Self.Channel.Send(fmt.Sprintf("%v has left\n", username), false)
				}
			})
		}
	}

	// Notify mumble mode about Discord user changes
	if l.Bridge.DiscordUserChange != nil {
		select {
		case l.Bridge.DiscordUserChange <- struct{}{}:
		default:
		}
	}

	// Update metrics
	promDiscordUsers.Set(float64(userCount))
}
