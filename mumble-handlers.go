package main

import (
	"strings"

	"layeh.com/gumble/gumble"
)

// MumbleListener Handle mumble events
type MumbleListener struct {
	Bridge *BridgeState
}

func (l *MumbleListener) mumbleConnect(e *gumble.ConnectEvent) {
	if l.Bridge.BridgeConfig.MumbleChannel != "" {
		//join specified channel
		startingChannel := e.Client.Channels.Find(l.Bridge.BridgeConfig.MumbleChannel)
		if startingChannel != nil {
			e.Client.Self.Move(startingChannel)
		}
	}
}

func (l *MumbleListener) mumbleUserChange(e *gumble.UserChangeEvent) {
	l.Bridge.MumbleUsersMutex.Lock()
	if e.Type.Has(gumble.UserChangeConnected) || e.Type.Has(gumble.UserChangeChannel) || e.Type.Has(gumble.UserChangeDisconnected) {
		l.Bridge.MumbleUsers = make(map[string]bool)
		for _, user := range l.Bridge.MumbleClient.Self.Channel.Users {
			//note, this might be too slow for really really big channels?
			//event listeners block while processing
			//also probably bad to rebuild the set every user change.
			if user.Name != l.Bridge.MumbleClient.Self.Name {
				l.Bridge.MumbleUsers[user.Name] = true
			}
		}
	}
	l.Bridge.MumbleUsersMutex.Unlock()

	if e.Type.Has(gumble.UserChangeConnected) {

		if !l.Bridge.BridgeConfig.MumbleDisableText {
			e.User.Send("Mumble-Discord-Bridge v" + version)

			// Tell the user who is connected to discord
			if len(l.Bridge.DiscordUsers) == 0 {
				e.User.Send("No users connected to Discord")
			} else {
				s := "Connected to Discord: "

				arr := []string{}
				l.Bridge.DiscordUsersMutex.Lock()
				for u := range l.Bridge.DiscordUsers {
					arr = append(arr, l.Bridge.DiscordUsers[u].username)
				}

				s = s + strings.Join(arr[:], ",")

				l.Bridge.DiscordUsersMutex.Unlock()
				e.User.Send(s)
			}
		}

		// Send discord a notice
		l.Bridge.discordSendMessageAll(e.User.Name + " has joined mumble")
	}
	if e.Type.Has(gumble.UserChangeDisconnected) {
		l.Bridge.discordSendMessageAll(e.User.Name + " has left mumble")
	}
}
