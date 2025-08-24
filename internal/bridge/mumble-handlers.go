package bridge

import (
	"fmt"
	"strings"
	"time"

	"github.com/stieneee/gumble/gumble"
)

// MumbleListener Handle mumble events
type MumbleListener struct {
	Bridge *BridgeState
}

func (l *MumbleListener) updateUsers() {
	l.Bridge.MumbleUsersMutex.Lock()
	l.Bridge.MumbleUsers = make(map[string]bool)
	for _, user := range l.Bridge.MumbleClient.Self.Channel.Users {
		//note, this might be too slow for really really big channels?
		//event listeners block while processing
		//also probably bad to rebuild the set every user change.
		if user.Name != l.Bridge.MumbleClient.Self.Name {
			l.Bridge.MumbleUsers[user.Name] = true
		}
	}
	promMumbleUsers.Set(float64(len(l.Bridge.MumbleUsers)))
	l.Bridge.MumbleUsersMutex.Unlock()

	// Notify metrics change for user count change
	l.Bridge.notifyMetricsChange()
}

func (l *MumbleListener) MumbleConnect(e *gumble.ConnectEvent) {
	l.Bridge.Logger.Info("MUMBLE_HANDLER", fmt.Sprintf("Connected to Mumble server: %s", e.Client.Conn.RemoteAddr()))
	l.Bridge.Logger.Debug("MUMBLE_HANDLER", fmt.Sprintf("Mumble client info: Username=%s, SessionID=%d", e.Client.Self.Name, e.Client.Self.Session))

	// Join specified channel
	if len(l.Bridge.BridgeConfig.MumbleChannel) > 0 {
		channelPath := strings.Join(l.Bridge.BridgeConfig.MumbleChannel, "/")
		l.Bridge.Logger.Info("MUMBLE_HANDLER", fmt.Sprintf("Attempting to join Mumble channel: %s", channelPath))

		startingChannel := e.Client.Channels.Find(l.Bridge.BridgeConfig.MumbleChannel...)
		if startingChannel != nil {
			l.Bridge.Logger.Debug("MUMBLE_HANDLER", fmt.Sprintf("Found target channel (ID: %d, Name: %s), moving to it", startingChannel.ID, startingChannel.Name))
			e.Client.Self.Move(startingChannel)
			l.Bridge.Logger.Info("MUMBLE_HANDLER", fmt.Sprintf("Successfully moved to Mumble channel: %s", channelPath))
		} else {
			l.Bridge.Logger.Warn("MUMBLE_HANDLER", fmt.Sprintf("Target Mumble channel not found: %s, staying in root channel", channelPath))
			l.logAvailableChannels(e.Client)
		}
	} else {
		l.Bridge.Logger.Debug("MUMBLE_HANDLER", "No specific Mumble channel specified, staying in root channel")
	}

	// l.updateUsers() // patch below

	// This is an ugly patch Mumble Client state is slow to update
	l.Bridge.Logger.Debug("MUMBLE_HANDLER", "Scheduling user list update in 5 seconds to allow Mumble state to stabilize")
	time.AfterFunc(5*time.Second, func() {
		defer func() {
			if r := recover(); r != nil {
				l.Bridge.Logger.Error("MUMBLE_HANDLER", fmt.Sprintf("Failed to update mumble user list: %v", r))
			}
		}()
		l.Bridge.Logger.Debug("MUMBLE_HANDLER", "Updating Mumble user list after connection stabilization")
		l.updateUsers()
	})
}

func (l *MumbleListener) MumbleUserChange(e *gumble.UserChangeEvent) {
	l.updateUsers()

	if e.Type.Has(gumble.UserChangeConnected) {

		l.Bridge.Logger.Info("MUMBLE_HANDLER", fmt.Sprintf("User connected to mumble: %s", e.User.Name))

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

				s = s + strings.Join(arr[:], ",")

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
	}
}

// logAvailableChannels logs all available channels on the Mumble server for debugging
func (l *MumbleListener) logAvailableChannels(client *gumble.Client) {
	l.Bridge.Logger.Info("MUMBLE_HANDLER", "Available channels on server:")
	l.logChannelTree(client.Channels[0], 0) // Start from root channel
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

// this function will support bot commands as well as user commands
func (l *MumbleListener) MumbleTextMessage(e *gumble.TextMessageEvent) {
	// ignore non-user messages
	if e.Sender == nil {
		l.Bridge.Logger.Warn("MUMBLE_HANDLER", "Received message from nil sender")
		return
	}

	// Ignore bot
	if e.Sender.Name == l.Bridge.MumbleClient.Self.Name {
		return
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
