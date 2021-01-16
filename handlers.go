package main

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"layeh.com/gumble/gumble"
)

//Listener holds references to the current BridgeConf
//and BridgeState for use by the event handlers
type Listener struct {
	BridgeConf    *BridgeConfig
	Bridge        *BridgeState
	UserCountLock *sync.Mutex
	ConnectedLock *sync.Mutex
}

func (l *Listener) ready(s *discordgo.Session, event *discordgo.Ready) {
	log.Println("READY event registered")
	//Setup initial discord state
	var g *discordgo.Guild
	g = nil
	for _, i := range event.Guilds {
		if i.ID == l.BridgeConf.GID {
			g = i
		}
	}
	if g == nil {
		log.Println("bad guild on READY")
		return
	}
	for _, vs := range g.VoiceStates {
		if vs.ChannelID == l.BridgeConf.CID {
			l.UserCountLock.Lock()
			l.Bridge.DiscordUserCount = l.Bridge.DiscordUserCount + 1
			u, err := s.User(vs.UserID)
			if err != nil {
				log.Println("error looking up username")
			}
			l.Bridge.DiscordUsers[u.Username] = true
			if l.Bridge.Connected {
				l.Bridge.Client.Do(func() {
					l.Bridge.Client.Self.Channel.Send(fmt.Sprintf("%v has joined Discord channel\n", u.Username), false)
				})
			}
			l.UserCountLock.Unlock()
		}
	}
}

func (l *Listener) messageCreate(s *discordgo.Session, m *discordgo.MessageCreate) {

	if l.Bridge.Mode == bridgeModeConstant {
		return
	}

	// Ignore all messages created by the bot itself
	if m.Author.ID == s.State.User.ID {
		return
	}
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
	prefix := "!" + l.BridgeConf.Command
	if strings.HasPrefix(m.Content, prefix+" link") {
		// Look for the message sender in that guild's current voice states.
		for _, vs := range g.VoiceStates {
			if vs.UserID == m.Author.ID {
				log.Printf("Trying to join GID %v and VID %v\n", g.ID, vs.ChannelID)
				die := make(chan bool)
				l.Bridge.ActiveConn = die
				go startBridge(s, g.ID, vs.ChannelID, l, die)
				return
			}
		}
	}

	if strings.HasPrefix(m.Content, prefix+" unlink") {
		// Look for the message sender in that guild's current voice states.
		for _, vs := range g.VoiceStates {
			if vs.UserID == m.Author.ID {
				log.Printf("Trying to leave GID %v and VID %v\n", g.ID, vs.ChannelID)
				l.Bridge.ActiveConn <- true
				l.Bridge.ActiveConn = nil
				return
			}
		}
	}

	if strings.HasPrefix(m.Content, prefix+" refresh") {
		// Look for the message sender in that guild's current voice states.
		for _, vs := range g.VoiceStates {
			if vs.UserID == m.Author.ID {
				log.Printf("Trying to refresh GID %v and VID %v\n", g.ID, vs.ChannelID)
				l.Bridge.ActiveConn <- true
				time.Sleep(5 * time.Second)
				l.Bridge.ActiveConn = make(chan bool)
				go startBridge(s, g.ID, vs.ChannelID, l, l.Bridge.ActiveConn)
				return
			}
		}
	}

	if strings.HasPrefix(m.Content, prefix+" auto") {
		if l.Bridge.Mode != bridgeModeAuto {
			l.Bridge.Mode = bridgeModeAuto
			l.Bridge.AutoChan = make(chan bool)
			go AutoBridge(s, l)
		} else {
			l.Bridge.AutoChan <- true
			l.Bridge.Mode = bridgeModeManual
		}
	}
}

func (l *Listener) guildCreate(s *discordgo.Session, event *discordgo.GuildCreate) {

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

func (l *Listener) voiceUpdate(s *discordgo.Session, event *discordgo.VoiceStateUpdate) {
	l.UserCountLock.Lock()
	if event.GuildID == l.BridgeConf.GID {
		if event.ChannelID == l.BridgeConf.CID {
			//get user
			u, err := s.User(event.UserID)
			if err != nil {
				log.Printf("error looking up user for uid %v", event.UserID)
			}
			//check to see if actually new user
			if l.Bridge.DiscordUsers[u.Username] {
				//not actually new user
				l.UserCountLock.Unlock()
				return
			}
			log.Println("user joined watched discord channel")
			l.ConnectedLock.Lock()
			if l.Bridge.Connected {
				l.Bridge.Client.Do(func() {
					l.Bridge.Client.Self.Channel.Send(fmt.Sprintf("%v has joined Discord channel\n", u.Username), false)
				})
			}
			l.ConnectedLock.Unlock()
			l.Bridge.DiscordUsers[u.Username] = true
			l.Bridge.DiscordUserCount = l.Bridge.DiscordUserCount + 1
			l.UserCountLock.Unlock()
		}
		if event.ChannelID == "" {
			//leave event, trigger recount of active users
			//TODO when next version of discordgo comes out, switch to PreviousState
			g, err := s.State.Guild(event.GuildID)
			if err != nil {
				// Could not find guild.
				l.UserCountLock.Unlock()
				return
			}

			// Look for current voice states in watched channel
			count := 0
			for _, vs := range g.VoiceStates {
				if vs.ChannelID == l.BridgeConf.CID {
					count = count + 1
				}
			}
			if l.Bridge.DiscordUserCount > count {
				u, err := s.User(event.UserID)
				if err != nil {
					log.Printf("error looking up user for uid %v", event.UserID)
				}
				delete(l.Bridge.DiscordUsers, u.Username)
				log.Println("user left watched discord channel")
				l.ConnectedLock.Lock()
				if l.Bridge.Connected {
					l.Bridge.Client.Do(func() {
						l.Bridge.Client.Self.Channel.Send(fmt.Sprintf("%v has left Discord channel\n", u.Username), false)
					})
				}
				l.ConnectedLock.Unlock()
				l.Bridge.DiscordUserCount = count
			}
			l.UserCountLock.Unlock()
		}

	}
	return
}

func (l *Listener) mumbleConnect(e *gumble.ConnectEvent) {
	if l.BridgeConf.MumbleChannel != "" {
		//join specified channel
		startingChannel := e.Client.Channels.Find(l.BridgeConf.MumbleChannel)
		if startingChannel != nil {
			e.Client.Self.Move(startingChannel)
		}
	}
}

func (l *Listener) mumbleUserChange(e *gumble.UserChangeEvent) {
	l.UserCountLock.Lock()
	if e.Type.Has(gumble.UserChangeConnected) || e.Type.Has(gumble.UserChangeChannel) || e.Type.Has(gumble.UserChangeDisconnected) {
		l.Bridge.MumbleUsers = make(map[string]bool)
		for _, user := range l.Bridge.Client.Self.Channel.Users {
			//note, this might be too slow for really really big channels?
			//event listeners block while processing
			//also probably bad to rebuild the set every user change.
			if user.Name != l.Bridge.Client.Self.Name {
				l.Bridge.MumbleUsers[user.Name] = true
			}
		}
	}
	l.UserCountLock.Unlock()
}
