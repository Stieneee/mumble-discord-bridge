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
	log.Printf("[DISCORD_HANDLER] MessageCreate called from Discord: %s from %s", m.Content, m.Author.Username)
	
	// Ignore all messages created by the bot itself
	if m.Author.ID == s.State.User.ID {
		log.Printf("[DISCORD_HANDLER] Ignoring message from self")
		return
	}
	
	// Find the channel that the message came from.
	var guildID string
	
	// Try to get channel from state cache first
	c, err := s.State.Channel(m.ChannelID)
	if err != nil {
		log.Printf("[DISCORD_HANDLER] Channel not in state cache: %s - Error: %v", m.ChannelID, err)
		log.Printf("[DISCORD_HANDLER] Falling back to direct guild ID from message: %s", m.GuildID)
		
		// If we can't find the channel in state, use the guild ID from the message directly
		guildID = m.GuildID
		
		// Try to fetch the channel directly from the API if we need to (optional)
		apiChannel, err := s.Channel(m.ChannelID)
		if err != nil {
			log.Printf("[DISCORD_HANDLER] Could not fetch channel via API: %s - Error: %v", m.ChannelID, err)
			// Continue with the guild ID from the message
		} else {
			log.Printf("[DISCORD_HANDLER] Successfully fetched channel via API: %s in guild %s", 
				apiChannel.ID, apiChannel.GuildID)
			c = apiChannel
		}
	} else {
		guildID = c.GuildID
		log.Printf("[DISCORD_HANDLER] Found channel %s in state cache", m.ChannelID)
	}
	
	// Find the guild for that channel
	var g *discordgo.Guild
	if c != nil && c.GuildID != "" {
		g, err = s.State.Guild(c.GuildID)
		if err != nil {
			log.Printf("[DISCORD_HANDLER] Could not find guild for channel: %s - Error: %v", c.ID, err)
			// Continue with guild ID from message
		}
	}
	
	// If we still don't have a guild, try to get it directly from the state using message's guild ID
	if g == nil && m.GuildID != "" {
		g, err = s.State.Guild(m.GuildID)
		if err != nil {
			log.Printf("[DISCORD_HANDLER] Could not find guild from message guild ID: %s - Error: %v", 
				m.GuildID, err)
			// Continue without guild object
		}
	}

	// Check if we have a valid guild to work with
	if g == nil {
		log.Printf("[DISCORD_HANDLER] Guild object is nil, comparing message guild ID: %s with expected guild: %s", 
			guildID, l.Bridge.BridgeConfig.GID)
		
		// Use the guild ID we extracted earlier
		if guildID != l.Bridge.BridgeConfig.GID {
			log.Printf("[DISCORD_HANDLER] Guild ID mismatch using message guild ID, ignoring message")
			return
		}
		
		log.Printf("[DISCORD_HANDLER] Guild ID matches configuration using message guild ID")
	} else {
		log.Printf("[DISCORD_HANDLER] Message from guild %s (%s), expected guild: %s", 
			g.Name, g.ID, l.Bridge.BridgeConfig.GID)
		
		// the guild has to match the config
		// If we maintain a single bridge per bot and guild this provides sufficient protection
		// If a user wants multiple bridges in one guild they will need to define multiple bots
		if g.ID != l.Bridge.BridgeConfig.GID {
			log.Printf("[DISCORD_HANDLER] Guild ID mismatch, ignoring message")
			return
		}
		
		log.Printf("[DISCORD_HANDLER] Guild ID matches configuration")
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
			_, err := l.Bridge.DiscordSession.ChannelMessageSend(m.ChannelID, s)
			if err != nil {
				log.Printf("Error sending command response: %v", err)
			}
		})

		// process the Discord specific command options

		if strings.HasPrefix(m.Content, prefix+" link") {
			if l.Bridge.Mode == BridgeModeConstant && strings.HasPrefix(m.Content, prefix) {
				_, err := l.Bridge.DiscordSession.ChannelMessageSend(m.ChannelID, "Constant mode enabled, link commands can not be entered")
				if err != nil {
					log.Printf("Error sending constant mode message: %v", err)
				}
				return
			}
			// Look for the message sender in that guild's current voice states.
			if bridgeConnected {
				_, err := l.Bridge.DiscordSession.ChannelMessageSend(m.ChannelID, "Bridge already running, unlink first")
				if err != nil {
					log.Printf("Error sending bridge status message: %v", err)
				}
				return
			}
			
			// If we have a guild object, check voice states
			if g != nil {
				// Look for the message sender in that guild's current voice states.
				for _, vs := range g.VoiceStates {
					if vs.UserID == m.Author.ID {
						log.Printf("Trying to join GID %v and VID %v\n", g.ID, vs.ChannelID)
						l.Bridge.DiscordChannelID = vs.ChannelID
						go l.Bridge.StartBridge()
						return
					}
				}
			} else {
				// We can't get voice states if we don't have a guild
				guild, err := s.Guild(guildID)
				if err != nil {
					log.Printf("Error fetching guild: %v", err)
					_, err := l.Bridge.DiscordSession.ChannelMessageSend(m.ChannelID, 
						"Couldn't detect your voice channel. Please join a voice channel first.")
					if err != nil {
						log.Printf("Error sending voice channel message: %v", err)
					}
					return
				}
				
				// Process the guild's voice states
				foundUser := false
				for _, vs := range guild.VoiceStates {
					if vs.UserID == m.Author.ID {
						foundUser = true
						if vs.ChannelID != "" {
							log.Printf("Trying to join GID %v and VID %v\n", guildID, vs.ChannelID)
							l.Bridge.DiscordChannelID = vs.ChannelID
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
					log.Printf("Error sending voice channel message: %v", err)
				}
			}
		}

		if strings.HasPrefix(m.Content, prefix+" unlink") {
			if l.Bridge.Mode == BridgeModeConstant && strings.HasPrefix(m.Content, prefix) {
				_, err := l.Bridge.DiscordSession.ChannelMessageSend(m.ChannelID, "Constant mode enabled, link commands can not be entered")
				if err != nil {
					log.Printf("Error sending constant mode message: %v", err)
				}
				return
			}
			// Look for the message sender in that guild's current voice states.
			if !bridgeConnected {
				_, err := l.Bridge.DiscordSession.ChannelMessageSend(m.ChannelID, "Bridge is not currently running")
				if err != nil {
					log.Printf("Error sending bridge status message: %v", err)
				}
				return
			}
			
			// Handle the case when guild might be nil
			if g == nil {
				log.Printf("Guild object is nil, allowing unlink for guild ID %v anyway\n", guildID)
				l.Bridge.BridgeDie <- true
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
				_, err := l.Bridge.DiscordSession.ChannelMessageSend(m.ChannelID, "Constant mode enabled, link commands can not be entered")
				if err != nil {
					log.Printf("Error sending constant mode message: %v", err)
				}
				return
			}
			// Look for the message sender in that guild's current voice states.
			if !bridgeConnected {
				_, err := l.Bridge.DiscordSession.ChannelMessageSend(m.ChannelID, "Bridge is not currently running")
				if err != nil {
					log.Printf("Error sending bridge status message: %v", err)
				}
				return
			}
			
			// Handle the case when guild might be nil
			if g == nil {
				log.Printf("Guild object is nil, allowing refresh for guild ID %v anyway\n", guildID)
				l.Bridge.BridgeDie <- true
				time.Sleep(5 * time.Second)
				go l.Bridge.StartBridge()
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

	} else if !strings.HasPrefix(m.Content, "!") {
		// Get a truncated version of the message for logs
		content := m.Content
		if len(content) > 50 {
			content = content[:47] + "..."
		}
		
		// Check if chat bridge is enabled
		if !l.Bridge.BridgeConfig.ChatBridge {
			log.Printf("[DISCORD→MUMBLE] Chat message received but ChatBridge is DISABLED")
			return
		}
		
		// Check if the bridge is connected
		l.Bridge.BridgeMutex.Lock()
		bridgeConnected := l.Bridge.Connected
		l.Bridge.BridgeMutex.Unlock()
		
		if !bridgeConnected {
			log.Printf("[DISCORD→MUMBLE] Chat message received but bridge is not connected")
			return
		}
		
		// Check if text messages to Mumble are disabled
		if l.Bridge.BridgeConfig.MumbleDisableText {
			log.Printf("[DISCORD→MUMBLE] Chat message received but MumbleDisableText is true")
			return
		}
		
		log.Printf("[DISCORD→MUMBLE] Forwarding message from %s: %s", m.Author.Username, content)
		
		// Perform null checks
		if l.Bridge.MumbleClient == nil || 
		   l.Bridge.MumbleClient.Self == nil || 
		   l.Bridge.MumbleClient.Self.Channel == nil {
			log.Printf("[DISCORD→MUMBLE] ERROR: Cannot forward message - MumbleClient is not properly initialized")
			return
		}
		
		// Format and send the message to Mumble
		message := fmt.Sprintf("%v: %v\n", m.Author.Username, m.Content)
		
		// Use a separate goroutine with timeout to make the call more resilient
		messageSent := make(chan bool, 1)
		go func() {
			l.Bridge.MumbleClient.Do(func() {
				l.Bridge.MumbleClient.Self.Channel.Send(message, false)
				messageSent <- true
			})
		}()
		
		// Wait for confirmation or timeout
		select {
		case <-messageSent:
			log.Printf("[DISCORD→MUMBLE] Successfully forwarded message")
		case <-time.After(2 * time.Second):
			log.Printf("[DISCORD→MUMBLE] ERROR: Timed out while trying to send message")
		}
	}
}

func (l *DiscordListener) VoiceUpdate(s *discordgo.Session, event *discordgo.VoiceStateUpdate) {
	l.Bridge.DiscordUsersMutex.Lock()
	defer l.Bridge.DiscordUsersMutex.Unlock()

	if event.GuildID == l.Bridge.BridgeConfig.GID {

		g, err := s.State.Guild(l.Bridge.BridgeConfig.GID)
		if err != nil {
			// Don't panic, just return since we can't proceed
			return
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