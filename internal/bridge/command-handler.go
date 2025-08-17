package bridge

import (
	"strings"
	"time"
)

// write a command handler that accepts a text command and a callback function
// the command handler should be able to handle the following commands:
// - help
// - version
// - link
// - unlink
// - refresh
// - auto
// - status
// - list

func (b *BridgeState) HandleCommand(msg string, userResponse func(string)) {

	b.Logger.Debug("COMMAND_HANDLER", "Handling command: "+msg)

	prefix := "!" + b.BridgeConfig.Command

	// TODO auto - toggle between auto and manual mode
	if strings.HasPrefix(msg, prefix+" help") {
		b.Logger.Debug("COMMAND_HANDLER", "Sending help message")
		userResponse(`Commands:
  help - display this message
	version - display the version of mumble-discord-bridge
	restart - force the bridge to restart
	status - display the current bridge 
	list - display the current users
	link (discord only) - link the bot to the current voice channel
	unlink (discord only) - unlink the bot from the current voice channel
	refresh (discord only) - refresh the bot's connection to the current voice channel`)
		return
	}

	if strings.HasPrefix(msg, prefix+" version") {
		userResponse("Mumble-Discord-Bridge " + b.BridgeConfig.Version)
		return
	}

	// TODO - rethink the mode switching commands
	// if strings.HasPrefix(msg, prefix+" auto") {
	// 	if b.Mode == BridgeModeConstant && strings.HasPrefix(msg, prefix) {
	// 		userResponse("Constant mode enabled, auto/manual commands can not be entered")
	// 		return
	// 	}

	// 	if b.Mode != BridgeModeAuto {
	// 		userResponse("Auto mode enabled")
	// 		b.Mode = BridgeModeAuto
	// 		b.DiscordChannelID = b.BridgeConfig.CID
	// 		b.AutoChanDie = make(chan bool)
	// 		go b.AutoBridge()
	// 	} else {
	// 		userResponse("Auto mode disabled")
	// 		b.AutoChanDie <- true
	// 		b.Mode = BridgeModeManual
	// 	}
	// 	return
	// }

	if strings.HasPrefix(msg, prefix+" restart") {
		userResponse("Restarting bridge")
		b.BridgeDie <- true
		return
	}

	if strings.HasPrefix(msg, prefix+" status") {
		if b.Connected {
			uptime := time.Since(b.StartTime).String()
			userResponse("Bridge is running, uptime: " + uptime + " mode: " + b.Mode.String())
		} else {
			userResponse("Bridge is not running")
		}
		return
	}

	if strings.HasPrefix(msg, prefix+" list") {
		// create a string of the connected users from the map
		// and send it to the user
		var discordUsers []string
		b.DiscordUsersMutex.Lock()
		for user := range b.DiscordUsers {
			discordUsers = append(discordUsers, b.DiscordUsers[user].username)
		}
		b.DiscordUsersMutex.Unlock()

		// mumble users
		b.MumbleUsersMutex.Lock()
		var mumbleUsers []string
		for user := range b.MumbleUsers {
			mumbleUsers = append(mumbleUsers, user)
		}

		userResponse("Discord users: " + strings.Join(discordUsers, ", ") + `
Mumble users: ` + strings.Join(mumbleUsers, ", "))
		return
	}
}
