package bridge

import (
	"fmt"
	"strings"

	"github.com/stieneee/gumble/gumble"
)

// MumbleListener Handle mumble events
type MumbleListener struct {
	Bridge *BridgeState
}

func (l *MumbleListener) updateUsers() {
	// Safely get connection manager
	connManager := l.Bridge.MumbleConnectionManager
	if connManager == nil {
		// No connection manager, clear users
		l.Bridge.MumbleUsersMutex.Lock()
		l.Bridge.MumbleUsers = make(map[string]bool)
		promMumbleUsers.Set(0)
		l.Bridge.MumbleUsersMutex.Unlock()

		return
	}

	// Safely get channel users and self name
	channelUsers := connManager.GetChannelUsers()
	selfName := connManager.GetSelfName()

	if len(channelUsers) == 0 && selfName != "" {
		// No users in channel but we have a self name, this might be temporary
		l.Bridge.MumbleUsersMutex.Lock()
		l.Bridge.MumbleUsers = make(map[string]bool)
		promMumbleUsers.Set(0)
		l.Bridge.MumbleUsersMutex.Unlock()

		return
	}

	if selfName == "" {
		// No self name, connection might not be ready
		l.Bridge.MumbleUsersMutex.Lock()
		l.Bridge.MumbleUsers = make(map[string]bool)
		promMumbleUsers.Set(0)
		l.Bridge.MumbleUsersMutex.Unlock()

		return
	}

	l.Bridge.MumbleUsersMutex.Lock()
	l.Bridge.MumbleUsers = make(map[string]bool)
	for _, user := range channelUsers {
		// note, this might be too slow for really really big channels?
		// event listeners block while processing
		// also probably bad to rebuild the set every user change.
		if user != nil && user.Name != "" && user.Name != selfName {
			l.Bridge.MumbleUsers[user.Name] = true
		}
	}
	promMumbleUsers.Set(float64(len(l.Bridge.MumbleUsers)))
	l.Bridge.MumbleUsersMutex.Unlock()

	// Notify metrics change for user count change
	l.Bridge.notifyMetricsChange()
}

// MumbleConnect handles Mumble connection events.
func (l *MumbleListener) MumbleConnect(e *gumble.ConnectEvent) {
	l.Bridge.Logger.Info("MUMBLE_HANDLER", fmt.Sprintf("Connected to Mumble server: %s", e.Client.Conn.RemoteAddr()))

	// Log client info using thread-safe access
	e.Client.Do(func() {
		if e.Client.Self != nil {
			l.Bridge.Logger.Debug("MUMBLE_HANDLER", fmt.Sprintf("Mumble client info: Username=%s, SessionID=%d", e.Client.Self.Name, e.Client.Self.Session))
		}
	})

	// Join specified channel using thread-safe access
	if len(l.Bridge.BridgeConfig.MumbleChannel) > 0 {
		channelPath := strings.Join(l.Bridge.BridgeConfig.MumbleChannel, "/")
		l.Bridge.Logger.Info("MUMBLE_HANDLER", fmt.Sprintf("Attempting to join Mumble channel: %s", channelPath))

		e.Client.Do(func() {
			startingChannel := e.Client.Channels.Find(l.Bridge.BridgeConfig.MumbleChannel...)
			if startingChannel != nil {
				l.Bridge.Logger.Debug("MUMBLE_HANDLER", fmt.Sprintf("Found target channel (ID: %d, Name: %s), moving to it", startingChannel.ID, startingChannel.Name))
				e.Client.Self.Move(startingChannel)
				l.Bridge.Logger.Info("MUMBLE_HANDLER", fmt.Sprintf("Successfully moved to Mumble channel: %s", channelPath))
			} else {
				l.Bridge.Logger.Warn("MUMBLE_HANDLER", fmt.Sprintf("Target Mumble channel not found: %s, staying in root channel", channelPath))
				l.logAvailableChannels(e.Client)
			}
		})
	} else {
		l.Bridge.Logger.Debug("MUMBLE_HANDLER", "No specific Mumble channel specified, staying in root channel")
	}

	// Update users immediately
	l.updateUsers()
}

// MumbleUserChange handles Mumble user change events.
func (l *MumbleListener) MumbleUserChange(e *gumble.UserChangeEvent) {
	l.updateUsers()

	if e.Type.Has(gumble.UserChangeConnected) {
		l.Bridge.Logger.Info("MUMBLE_HANDLER", fmt.Sprintf("User connected to mumble: %s", e.User.Name))

		// Emit user joined event
		l.Bridge.EmitUserEvent("mumble", 0, e.User.Name, nil)

		if !l.Bridge.BridgeConfig.MumbleDisableText {
			e.User.Send("Mumble-Discord-Bridge " + l.Bridge.BridgeConfig.Version)

			// Tell the user who is connected to discord
			l.Bridge.DiscordUsersMutex.Lock()
			if len(l.Bridge.DiscordUsers) == 0 {
				e.User.Send("No users connected to Discord")
			} else {
				s := "Connected to Discord: "

				arr := []string{}
				for u := range l.Bridge.DiscordUsers {
					arr = append(arr, l.Bridge.DiscordUsers[u].username)
				}

				s += strings.Join(arr, ",")

				e.User.Send(s)
			}
			l.Bridge.DiscordUsersMutex.Unlock()
		}

		// Send discord a notice
		l.Bridge.discordSendMessage(e.User.Name + " has joined mumble")
	}

	if e.Type.Has(gumble.UserChangeDisconnected) {
		l.Bridge.discordSendMessage(e.User.Name + " has left mumble")
		l.Bridge.Logger.Info("MUMBLE_HANDLER", fmt.Sprintf("User disconnected from mumble: %s", e.User.Name))

		// Emit user left event
		l.Bridge.EmitUserEvent("mumble", 1, e.User.Name, nil)
	}
}

// logAvailableChannels logs all available channels on the Mumble server for debugging
func (l *MumbleListener) logAvailableChannels(client *gumble.Client) {
	l.Bridge.Logger.Info("MUMBLE_HANDLER", "Available channels on server:")
	client.Do(func() {
		if rootChannel := client.Channels[0]; rootChannel != nil {
			l.logChannelTree(rootChannel, 0)
		}
	})
}

// logChannelTree recursively logs the channel tree structure
func (l *MumbleListener) logChannelTree(channel *gumble.Channel, depth int) {
	if channel == nil {
		return
	}

	// Create indentation based on depth
	indent := strings.Repeat("  ", depth)

	// Log this channel
	l.Bridge.Logger.Info("MUMBLE_HANDLER", fmt.Sprintf("%s- %s (ID: %d)", indent, channel.Name, channel.ID))

	// Log child channels
	for _, child := range channel.Children {
		l.logChannelTree(child, depth+1)
	}
}

// MumbleTextMessage handles text messages from Mumble, supporting both bot commands and user messages.
func (l *MumbleListener) MumbleTextMessage(e *gumble.TextMessageEvent) {
	// ignore non-user messages
	if e.Sender == nil {
		l.Bridge.Logger.Warn("MUMBLE_HANDLER", "Received message from nil sender")

		return
	}

	// Ignore bot - safely check if sender is self
	if l.Bridge.MumbleConnectionManager != nil {
		selfName := l.Bridge.MumbleConnectionManager.GetSelfName()
		if selfName != "" && e.Sender.Name == selfName {
			return
		}
	}

	// check for bot commands
	// the HandleCommand function is also used by the discord listener
	if strings.HasPrefix(e.Message, "!") {
		// check if mumble command is enabled
		if !l.Bridge.BridgeConfig.MumbleCommand {
			return
		}

		l.Bridge.HandleCommand(e.Message, func(res string) {
			// send the response to the user
			e.Sender.Send(res)
		})
	} else {
		l.Bridge.discordSendMessage(e.Sender.Name + ": " + e.Message)
	}
}
