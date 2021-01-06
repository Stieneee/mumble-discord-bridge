package main

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"layeh.com/gumble/gumble"
)

type Listener struct {
	BridgeConf *BridgeConfig
	Bridge     *BridgeState
}

func ready(s *discordgo.Session, event *discordgo.Ready) {
	log.Println("READY event registered")
}

func messageCreate(s *discordgo.Session, m *discordgo.MessageCreate) {

	if BridgeConf.Mode == BridgeModeConstant {
		return
	}

	// Ignore all messages created by the bot itself
	if m.Author.ID == s.State.User.ID {
		return
	}
	prefix := "!" + BridgeConf.Command
	if strings.HasPrefix(m.Content, prefix+" link") {

		// Find the channel that the message came from.
		c, err := s.State.Channel(m.ChannelID)
		if err != nil {
			// Could not find channel.
			return
		}

		// Find the guild for that channel.
		g, err := s.State.Guild(c.GuildID)
		if err != nil {
			// Could not find guild.
			return
		}

		// Look for the message sender in that guild's current voice states.
		for _, vs := range g.VoiceStates {
			if vs.UserID == m.Author.ID {
				log.Printf("Trying to join GID %v and VID %v\n", g.ID, vs.ChannelID)
				die := make(chan bool)
				Bridge.ActiveConn = die
				//go startBridge(s, g.ID, vs.ChannelID, BridgeConf.Config, BridgeConf.MumbleAddr, BridgeConf.MumbleInsecure, die)
				return
			}
		}
	}

	if strings.HasPrefix(m.Content, prefix+" unlink") {

		// Find the channel that the message came from.
		c, err := s.State.Channel(m.ChannelID)
		if err != nil {
			// Could not find channel.
			return
		}

		// Find the guild for that channel.
		g, err := s.State.Guild(c.GuildID)
		if err != nil {
			// Could not find guild.
			return
		}

		// Look for the message sender in that guild's current voice states.
		for _, vs := range g.VoiceStates {
			if vs.UserID == m.Author.ID {
				log.Printf("Trying to leave GID %v and VID %v\n", g.ID, vs.ChannelID)
				Bridge.ActiveConn <- true
				Bridge.ActiveConn = nil
				MumbleReset()
				DiscordReset()
				return
			}
		}
	}

	if strings.HasPrefix(m.Content, prefix+" refresh") {

		// Find the channel that the message came from.
		c, err := s.State.Channel(m.ChannelID)
		if err != nil {
			// Could not find channel.
			return
		}

		// Find the guild for that channel.
		g, err := s.State.Guild(c.GuildID)
		if err != nil {
			// Could not find guild.
			return
		}

		// Look for the message sender in that guild's current voice states.
		for _, vs := range g.VoiceStates {
			if vs.UserID == m.Author.ID {
				log.Printf("Trying to refresh GID %v and VID %v\n", g.ID, vs.ChannelID)
				Bridge.ActiveConn <- true
				MumbleReset()
				DiscordReset()
				time.Sleep(5 * time.Second)
				Bridge.ActiveConn = make(chan bool)
				//go startBridge(s, g.ID, vs.ChannelID, BridgeConf.Config, BridgeConf.MumbleAddr, BridgeConf.MumbleInsecure, Bridge.ActiveConn)
				return
			}
		}
	}

	if strings.HasPrefix(m.Content, prefix+" auto") {
		if BridgeConf.Mode != BridgeModeAuto {
			BridgeConf.Mode = BridgeModeAuto
			Bridge.AutoChan = make(chan bool)
			//go AutoBridge(s)
		} else {
			Bridge.AutoChan <- true
			BridgeConf.Mode = BridgeModeManual
		}
	}
}

func guildCreate(s *discordgo.Session, event *discordgo.GuildCreate) {

	if event.Guild.Unavailable {
		return
	}

	for _, channel := range event.Guild.Channels {
		if channel.ID == event.Guild.ID {
			log.Println("Mumble-Discord bridge is active in new guild")
			return
		}
	}
}

func voiceUpdate(s *discordgo.Session, event *discordgo.VoiceStateUpdate) {
	if event.GuildID == BridgeConf.GID {
		if event.ChannelID == BridgeConf.CID {
			//get user
			u, err := s.User(event.UserID)
			if err != nil {
				log.Printf("error looking up user for uid %v", event.UserID)
			}
			//check to see if actually new user
			if Bridge.DiscordUsers[u.Username] {
				//not actually new user
				return
			}
			log.Println("user joined watched discord channel")
			if Bridge.Connected {
				Bridge.Client.Do(func() {
					Bridge.Client.Self.Channel.Send(fmt.Sprintf("%v has joined Discord channel\n", u.Username), false)
				})
			}
			Bridge.DiscordUsers[u.Username] = true
			log.Println(Bridge.DiscordUsers)
			Bridge.DiscordUserCount = Bridge.DiscordUserCount + 1
		}
		if event.ChannelID == "" {
			//leave event, trigger recount of active users
			//TODO when next version of discordgo comes out, switch to PreviousState
			g, err := s.State.Guild(event.GuildID)
			if err != nil {
				// Could not find guild.
				return
			}

			// Look for current voice states in watched channel
			count := 0
			for _, vs := range g.VoiceStates {
				if vs.ChannelID == BridgeConf.CID {
					count = count + 1
				}
			}
			if Bridge.DiscordUserCount > count {
				u, err := s.User(event.UserID)
				if err != nil {
					log.Printf("error looking up user for uid %v", event.UserID)
				}
				delete(Bridge.DiscordUsers, u.Username)
				log.Println("user left watched discord channel")
				if Bridge.Connected {
					Bridge.Client.Do(func() {
						Bridge.Client.Self.Channel.Send(fmt.Sprintf("%v has left Discord channel\n", u.Username), false)
					})
				}
				Bridge.DiscordUserCount = count
			}
		}

	}
	return
}

func (l *Listener) mumbleConnect(e *gumble.ConnectEvent) {
	if l.BridgeConf.MumbleChannel != "" {
		//join specified channel
		startingChannel := e.Client.Channels.Find(BridgeConf.MumbleChannel)
		if startingChannel != nil {
			e.Client.Self.Move(startingChannel)
		}
	}
}

func (l *Listener) mumbleUserChange(e *gumble.UserChangeEvent) {
	if e.Type.Has(gumble.UserChangeConnected) || e.Type.Has(gumble.UserChangeChannel) || e.Type.Has(gumble.UserChangeDisconnected) {
		Bridge.MumbleUsers = make(map[string]bool)
		for _, user := range l.Bridge.Client.Self.Channel.Users {
			//note, this might be too slow for really really big channels?
			//event listeners block while processing
			//also probably bad to rebuild the set every user change.
			if user.Name != l.Bridge.Client.Self.Name {
				l.Bridge.MumbleUsers[user.Name] = true
			}
		}
	}
}
