package bridge

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
)

// DiscordListener holds references to the current BridgeConf
// and BridgeState for use by the event handlers
type DiscordListener struct {
	Bridge *BridgeState
}

func (l *DiscordListener) GuildCreate(s *discordgo.Session, event *discordgo.GuildCreate) {
	if event.ID != l.Bridge.BridgeConfig.GID {
		log.Println("Received GuildCreate from a guild not in config")
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
				log.Println("Error looking up username")
			}

			dm, err := s.UserChannelCreate(u.ID)
			if err != nil {
				log.Println("Error creating private channel for", u.Username)
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
			if l.Bridge.Connected && !l.Bridge.BridgeConfig.MumbleDisableText {
				l.Bridge.MumbleClient.Do(func() {
					l.Bridge.MumbleClient.Self.Channel.Send(fmt.Sprintf("%v has joined Discord\n", u.Username), false)
				})
			}
			l.Bridge.BridgeMutex.Unlock()

		}
	}
}

func (l *DiscordListener) MessageCreate(s *discordgo.Session, m *discordgo.MessageCreate) {
	// log the entire message structure
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

	// the guild has to match the config
	// If we maintain a single bridge per bot and guild this provides sufficient protection
	// If a user wants multiple bridges in one guild they will need to define multiple bots
	if g.ID != l.Bridge.BridgeConfig.GID {
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

		// process the shared command options
		l.Bridge.HandleCommand(m.Content, func(s string) {
			l.Bridge.DiscordSession.ChannelMessageSend(m.ChannelID, s)
		})

		// process the Discord specific command options

		if strings.HasPrefix(m.Content, prefix+" link") {
			if l.Bridge.Mode == BridgeModeConstant && strings.HasPrefix(m.Content, prefix) {
				l.Bridge.DiscordSession.ChannelMessageSend(m.ChannelID, "Constant mode enabled, link commands can not be entered")
				return
			}
			// Look for the message sender in that guild's current voice states.
			for _, vs := range g.VoiceStates {
				if bridgeConnected {
					l.Bridge.DiscordSession.ChannelMessageSend(m.ChannelID, "Bridge already running, unlink first")
					return
				}
				if vs.UserID == m.Author.ID {
					log.Printf("Trying to join GID %v and VID %v\n", g.ID, vs.ChannelID)
					l.Bridge.DiscordChannelID = vs.ChannelID
					go l.Bridge.StartBridge()
					return
				}
			}
		}

		if strings.HasPrefix(m.Content, prefix+" unlink") {
			if l.Bridge.Mode == BridgeModeConstant && strings.HasPrefix(m.Content, prefix) {
				l.Bridge.DiscordSession.ChannelMessageSend(m.ChannelID, "Constant mode enabled, link commands can not be entered")
				return
			}
			// Look for the message sender in that guild's current voice states.
			if !bridgeConnected {
				l.Bridge.DiscordSession.ChannelMessageSend(m.ChannelID, "Bridge is not currently running")
				return
			}
			for _, vs := range g.VoiceStates {
				if vs.UserID == m.Author.ID && vs.ChannelID == l.Bridge.DiscordChannelID {
					log.Printf("Trying to leave GID %v and VID %v\n", g.ID, vs.ChannelID)
					l.Bridge.BridgeDie <- true
					return
				}
			}
		}

		if strings.HasPrefix(m.Content, prefix+" refresh") {
			if l.Bridge.Mode == BridgeModeConstant && strings.HasPrefix(m.Content, prefix) {
				l.Bridge.DiscordSession.ChannelMessageSend(m.ChannelID, "Constant mode enabled, link commands can not be entered")
				return
			}
			// Look for the message sender in that guild's current voice states.
			if !bridgeConnected {
				l.Bridge.DiscordSession.ChannelMessageSend(m.ChannelID, "Bridge is not currently running")
				return
			}
			for _, vs := range g.VoiceStates {
				if vs.UserID == m.Author.ID {
					log.Printf("Trying to refresh GID %v and VID %v\n", g.ID, vs.ChannelID)
					l.Bridge.BridgeDie <- true

					time.Sleep(5 * time.Second)

					go l.Bridge.StartBridge()
					return
				}
			}
		}

	} else if !strings.HasPrefix(m.Content, "!") && l.Bridge.BridgeConfig.ChatBridge {
		if l.Bridge.Connected {
			l.Bridge.MumbleClient.Do(func() {
				l.Bridge.MumbleClient.Self.Channel.Send(fmt.Sprintf("%v: %v\n", m.Author.Username, m.Content), false)
			})
		}
	}
}

func (l *DiscordListener) VoiceUpdate(s *discordgo.Session, event *discordgo.VoiceStateUpdate) {
	l.Bridge.DiscordUsersMutex.Lock()
	defer l.Bridge.DiscordUsersMutex.Unlock()

	if event.GuildID == l.Bridge.BridgeConfig.GID {

		g, err := s.State.Guild(l.Bridge.BridgeConfig.GID)
		if err != nil {
			log.Println("Error finding guild")
			panic(err)
		}

		for u := range l.Bridge.DiscordUsers {
			du := l.Bridge.DiscordUsers[u]
			du.seen = false
			l.Bridge.DiscordUsers[u] = du
		}

		// Sync the channel voice states to the local discordUsersMap
		for _, vs := range g.VoiceStates {
			if vs.ChannelID == l.Bridge.DiscordChannelID {
				if s.State.User.ID == vs.UserID {
					// Ignore bot
					continue
				}

				if _, ok := l.Bridge.DiscordUsers[vs.UserID]; !ok {

					u, err := s.User(vs.UserID)
					if err != nil {
						log.Println("Error looking up username")
						continue
					}

					log.Println("User joined Discord " + u.Username)
					dm, err := s.UserChannelCreate(u.ID)
					if err != nil {
						log.Println("Error creating private channel for", u.Username)
					}
					l.Bridge.DiscordUsers[vs.UserID] = DiscordUser{
						username: u.Username,
						seen:     true,
						dm:       dm,
					}
					l.Bridge.BridgeMutex.Lock()
					if l.Bridge.Connected && !l.Bridge.BridgeConfig.MumbleDisableText {
						l.Bridge.MumbleClient.Do(func() {
							l.Bridge.MumbleClient.Self.Channel.Send(fmt.Sprintf("%v has joined Discord\n", u.Username), false)
						})
					}
					l.Bridge.BridgeMutex.Unlock()
				} else {
					du := l.Bridge.DiscordUsers[vs.UserID]
					du.seen = true
					l.Bridge.DiscordUsers[vs.UserID] = du
				}

			}
		}

		// Remove users that are no longer connected
		for id := range l.Bridge.DiscordUsers {
			if !l.Bridge.DiscordUsers[id].seen {
				log.Println("User left Discord channel " + l.Bridge.DiscordUsers[id].username)
				l.Bridge.BridgeMutex.Lock()
				if l.Bridge.Connected && !l.Bridge.BridgeConfig.MumbleDisableText {
					l.Bridge.MumbleClient.Do(func() {
						l.Bridge.MumbleClient.Self.Channel.Send(fmt.Sprintf("%v has left Discord channel\n", l.Bridge.DiscordUsers[id].username), false)
					})
				}
				delete(l.Bridge.DiscordUsers, id)
				l.Bridge.BridgeMutex.Unlock()
			}
		}

		l.Bridge.BridgeMutex.Lock()
		promDiscordUsers.Set(float64(len(l.Bridge.DiscordUsers)))
		l.Bridge.BridgeMutex.Unlock()
	}
}
