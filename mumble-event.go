package main

import (
	"fmt"
	"strings"

	"github.com/bwmarrin/discordgo"
	"layeh.com/gumble/gumble"
	_ "layeh.com/gumble/opus"
)

// MumbleEventListener - Bridge Event Handler
type MumbleEventListener struct {
	d *discordgo.Session
}

// OnConnect - event handler
func (ml MumbleEventListener) OnConnect(e *gumble.ConnectEvent) {
	fmt.Println("OnConnect", e)
}

// OnDisconnect - event handler
func (ml MumbleEventListener) OnDisconnect(e *gumble.DisconnectEvent) {
	fmt.Println("OnDisconnect", e)
}

// OnTextMessage - event handler
func (ml MumbleEventListener) OnTextMessage(e *gumble.TextMessageEvent) {
	fmt.Println("OnTextMessage", e)
}

// OnUserChange - event handler
func (ml MumbleEventListener) OnUserChange(e *gumble.UserChangeEvent) {
	fmt.Println("OnUserChange", e.User.Name, e)
	if e.Type.Has(gumble.UserChangeConnected) {
		e.User.Send("Mumble-Discord-Bridge v" + version)

		// Tell the user who is connected to discord
		if len(discordUsers) == 0 {
			e.User.Send("No users connected to Discord")
		} else {
			s := "Users connected to Discord: "

			arr := []string{}
			discordUsersMutex.Lock()
			for u := range discordUsers {
				arr = append(arr, u)
			}

			s = s + strings.Join(arr[:], ",")

			discordUsersMutex.Unlock()
			e.User.Send(s)
		}

		// Send discord a notice
		discordSendMessageAll(ml.d, e.User.Name+" has joined mumble")
	}
	if e.Type.Has(gumble.UserChangeDisconnected) {
		discordSendMessageAll(ml.d, e.User.Name+" has left mumble")
	}
}

// OnChannelChange - event handler
func (ml MumbleEventListener) OnChannelChange(e *gumble.ChannelChangeEvent) {
	fmt.Println("OnChannelChange", e)
}

// OnPermissionDenied - event handler
func (ml MumbleEventListener) OnPermissionDenied(e *gumble.PermissionDeniedEvent) {
	fmt.Println("OnPermissionDenied", e)
}

// OnUserList - event handler
func (ml MumbleEventListener) OnUserList(e *gumble.UserListEvent) {
	fmt.Println("OnUserList", e)
}

// OnACL - event handler
func (ml MumbleEventListener) OnACL(e *gumble.ACLEvent) {
	fmt.Println("OnACL", e)
}

// OnBanList - event handler
func (ml MumbleEventListener) OnBanList(e *gumble.BanListEvent) {
	fmt.Println("OnBanList", e)
}

// OnContextActionChange - event handler
func (ml MumbleEventListener) OnContextActionChange(e *gumble.ContextActionChangeEvent) {
	fmt.Println("OnContextActionChange", e)
}

// OnServerConfig - event handler
func (ml MumbleEventListener) OnServerConfig(e *gumble.ServerConfigEvent) {
	fmt.Println("OnServerConfig", e)
}
