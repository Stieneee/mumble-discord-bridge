package bridge

import (
	"log"
	"strings"

	"github.com/stieneee/gumble/gumble"
)

// MumbleListener Handle mumble events
type MumbleListener struct {
	Bridge *BridgeState
}

func (l *MumbleListener) MumbleConnect(e *gumble.ConnectEvent) {
	//join specified channel
	startingChannel := e.Client.Channels.Find(l.Bridge.BridgeConfig.MumbleChannel...)
	if startingChannel != nil {
		e.Client.Self.Move(startingChannel)
	}
}

func (l *MumbleListener) MumbleUserChange(e *gumble.UserChangeEvent) {
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

		log.Println("User connected to mumble " + e.User.Name)

		if !l.Bridge.BridgeConfig.MumbleDisableText {
			e.User.Send("Mumble-Discord-Bridge v" + l.Bridge.BridgeConfig.Version)

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
		l.Bridge.discordSendMessageAll(e.User.Name + " has joined mumble")
	}

	if e.Type.Has(gumble.UserChangeDisconnected) {
		l.Bridge.discordSendMessageAll(e.User.Name + " has left mumble")
		log.Println("User disconnected from mumble " + e.User.Name)
	}
}
