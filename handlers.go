package main

import (
	"log"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
)

func ready(s *discordgo.Session, event *discordgo.Ready) {
	log.Println("READY event registered")
}

func messageCreate(s *discordgo.Session, m *discordgo.MessageCreate) {

	// Ignore all messages created by the bot itself
	// This isn't required in this specific example but it's a good practice.
	log.Println("Checking message")
	if m.Author.ID == s.State.User.ID {
		return
	}

	if strings.HasPrefix(m.Content, "!yammer link") {

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
				YBConfig.ActiveConns[vs.ChannelID] = die
				go startBridge(s, g.ID, vs.ChannelID, YBConfig.Config, YBConfig.MumbleAddr, YBConfig.MumbleInsecure, die)
				return
			}
		}
	}

	if strings.HasPrefix(m.Content, "!yammer unlink") {

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
				YBConfig.ActiveConns[vs.ChannelID] <- true
				YBConfig.ActiveConns[vs.ChannelID] = nil
				MumbleReset()
				DiscordReset()
				return
			}
		}
	}

	if strings.HasPrefix(m.Content, "!yammer refresh") {

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
				YBConfig.ActiveConns[vs.ChannelID] <- true
				MumbleReset()
				DiscordReset()
				time.Sleep(5 * time.Second)
				YBConfig.ActiveConns[vs.ChannelID] = make(chan bool)
				go startBridge(s, g.ID, vs.ChannelID, YBConfig.Config, YBConfig.MumbleAddr, YBConfig.MumbleInsecure, YBConfig.ActiveConns[vs.ChannelID])
				return
			}
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
