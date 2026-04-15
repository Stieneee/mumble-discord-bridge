package bridge

import (
	"fmt"
	"strings"

	"github.com/bwmarrin/discordgo"
)

// Slash command definitions for Discord

// ConnectCommand is the /connect slash command
var ConnectCommand = &discordgo.ApplicationCommand{
	Name:                     "connect",
	Description:              "Manually connect the bridge (manual mode only)",
	DefaultMemberPermissions: discordgo.PermissionManageServer,
}

// DisconnectCommand is the /disconnect slash command
var DisconnectCommand = &discordgo.ApplicationCommand{
	Name:                     "disconnect",
	Description:              "Manually disconnect the bridge (manual mode only)",
	DefaultMemberPermissions: discordgo.PermissionManageServer,
}

// ModeCommand is the /mode slash command
var ModeCommand = &discordgo.ApplicationCommand{
	Name:                     "mode",
	Description:              "Set the bridge mode (auto/manual/constant)",
	DefaultMemberPermissions: discordgo.PermissionManageServer,
	Options: []*discordgo.ApplicationCommandOption{
		{
			Name:         "mode",
			Description:  "The bridge mode to set",
			Type:         discordgo.ApplicationCommandOptionString,
			Required:     true,
			Autocomplete: false,
			Choices: []*discordgo.ApplicationCommandOptionChoice{
				{
					Name:  "auto",
					Value: "auto",
				},
				{
					Name:  "manual",
					Value: "manual",
				},
				{
					Name:  "constant",
					Value: "constant",
				},
			},
		},
	},
}

// UsersCommand is the /users slash command
var UsersCommand = &discordgo.ApplicationCommand{
	Name:        "users",
	Description: "List users on both Mumble and Discord",
}

// AllSlashCommands holds all slash commands for registration
var AllSlashCommands = []*discordgo.ApplicationCommand{
	ConnectCommand,
	DisconnectCommand,
	ModeCommand,
	UsersCommand,
}

// IsUserAdmin checks if a user is in the admin list
// For Discord: checks userID against DiscordAdminIDs
// For Mumble: checks username against MumbleAdminNames
func (b *BridgeState) IsUserAdmin(userID, username string) bool {
	// Check Discord admin list
	for _, adminID := range b.BridgeConfig.DiscordAdminIDs {
		if userID == adminID {
			return true
		}
	}

	// Check Mumble admin list
	for _, adminName := range b.BridgeConfig.MumbleAdminNames {
		if username == adminName {
			return true
		}
	}

	return false
}

// interactionUser extracts the user ID and display name from an interaction.
func interactionUser(i *discordgo.InteractionCreate) (userID, username string) {
	if i.Member != nil && i.Member.User != nil {
		userID = i.Member.User.ID
		username = i.Member.Nick
		if username == "" {
			username = i.Member.User.Username
		}
	} else if i.User != nil {
		userID = i.User.ID
		username = i.User.Username
	}
	return
}

// HandleSlashCommand processes slash command interactions
func (b *BridgeState) HandleSlashCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	b.Logger.Debug("SLASH_COMMAND", fmt.Sprintf("Received interaction: %s", i.ApplicationCommandData().Name))

	userID, username := interactionUser(i)
	b.Logger.Debug("SLASH_COMMAND", fmt.Sprintf("User: %s (ID: %s)", username, userID))

	// Route to appropriate command handler
	switch i.ApplicationCommandData().Name {
	case "connect":
		b.handleSlashConnect(s, i)
	case "disconnect":
		b.handleSlashDisconnect(s, i)
	case "mode":
		b.handleSlashMode(s, i)
	case "users":
		b.handleSlashUsers(s, i)
	default:
		b.Logger.Warn("SLASH_COMMAND", fmt.Sprintf("Unknown command: %s", i.ApplicationCommandData().Name))
	}
}

// handleSlashConnect handles the /connect command
func (b *BridgeState) handleSlashConnect(s *discordgo.Session, i *discordgo.InteractionCreate) {
	userID, username := interactionUser(i)

	// Check admin permission
	if !b.IsUserAdmin(userID, username) {
		b.Logger.Info("SLASH_COMMAND", fmt.Sprintf("User %s attempted /connect without admin permissions", username))
		resp := &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "You don't have permission to use this command.",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		}
		_ = s.InteractionRespond(i, resp)
		return
	}

	// Check mode
	if b.Mode == BridgeModeConstant {
		resp := &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "Constant mode enabled, connect command not available.",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		}
		_ = s.InteractionRespond(i, resp)
		return
	}

	// Check if already connected
	b.BridgeMutex.Lock()
	connected := b.Connected
	b.BridgeMutex.Unlock()

	if connected {
		resp := &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "Bridge already running.",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		}
		_ = s.InteractionRespond(i, resp)
		return
	}

	// Start bridge
	b.Logger.Info("SLASH_COMMAND", fmt.Sprintf("User %s triggering bridge connect", username))
	go b.StartBridge()

	resp := &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: "Bridge connecting...",
		},
	}
	_ = s.InteractionRespond(i, resp)
}

// handleSlashDisconnect handles the /disconnect command
func (b *BridgeState) handleSlashDisconnect(s *discordgo.Session, i *discordgo.InteractionCreate) {
	userID, username := interactionUser(i)

	// Check admin permission
	if !b.IsUserAdmin(userID, username) {
		b.Logger.Info("SLASH_COMMAND", fmt.Sprintf("User %s attempted /disconnect without admin permissions", username))
		resp := &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "You don't have permission to use this command.",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		}
		_ = s.InteractionRespond(i, resp)
		return
	}

	// Check mode
	if b.Mode == BridgeModeConstant {
		resp := &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "Constant mode enabled, disconnect command not available.",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		}
		_ = s.InteractionRespond(i, resp)
		return
	}

	// Check if not connected
	b.BridgeMutex.Lock()
	connected := b.Connected
	b.BridgeMutex.Unlock()

	if !connected {
		resp := &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "Bridge is not running.",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		}
		_ = s.InteractionRespond(i, resp)
		return
	}

	// Stop bridge
	b.Logger.Info("SLASH_COMMAND", fmt.Sprintf("User %s triggering bridge disconnect", username))
	go b.StopBridge()

	resp := &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: "Bridge disconnecting...",
		},
	}
	_ = s.InteractionRespond(i, resp)
}

// handleSlashMode handles the /mode command
func (b *BridgeState) handleSlashMode(s *discordgo.Session, i *discordgo.InteractionCreate) {
	userID, username := interactionUser(i)

	// Check admin permission
	if !b.IsUserAdmin(userID, username) {
		b.Logger.Info("SLASH_COMMAND", fmt.Sprintf("User %s attempted /mode without admin permissions", username))
		resp := &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "You don't have permission to use this command.",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		}
		_ = s.InteractionRespond(i, resp)
		return
	}

	// Get the mode option
	options := i.ApplicationCommandData().Options
	modeOption := ""
	for _, opt := range options {
		if opt.Name == "mode" {
			modeOption = opt.StringValue()
			break
		}
	}

	if modeOption == "" {
		resp := &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "Invalid mode specified.",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		}
		_ = s.InteractionRespond(i, resp)
		return
	}

	// Set the mode
	oldMode := b.Mode.String()
	switch modeOption {
	case "auto":
		b.Mode = BridgeModeAuto
	case "manual":
		b.Mode = BridgeModeManual
	case "constant":
		b.Mode = BridgeModeConstant
	default:
		resp := &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "Invalid mode. Use auto, manual, or constant.",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		}
		_ = s.InteractionRespond(i, resp)
		return
	}

	b.Logger.Info("SLASH_COMMAND", fmt.Sprintf("User %s changed mode from %s to %s", username, oldMode, b.Mode.String()))

	resp := &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: fmt.Sprintf("Bridge mode changed from %s to %s", oldMode, b.Mode.String()),
		},
	}
	_ = s.InteractionRespond(i, resp)
}

// handleSlashUsers handles the /users command (no admin required)
func (b *BridgeState) handleSlashUsers(s *discordgo.Session, i *discordgo.InteractionCreate) {
	// Get Discord users
	b.DiscordUsersMutex.Lock()
	var discordUsers []string
	for _, u := range b.DiscordUsers {
		discordUsers = append(discordUsers, u.username)
	}
	b.DiscordUsersMutex.Unlock()

	// Get Mumble users
	b.MumbleUsersMutex.Lock()
	var mumbleUsers []string
	for user := range b.MumbleUsers {
		mumbleUsers = append(mumbleUsers, user)
	}
	b.MumbleUsersMutex.Unlock()

	// Format response
	discordList := "No users"
	if len(discordUsers) > 0 {
		discordList = strings.Join(discordUsers, ", ")
	}

	mumbleList := "No users"
	if len(mumbleUsers) > 0 {
		mumbleList = strings.Join(mumbleUsers, ", ")
	}

	content := fmt.Sprintf("**Discord users:** %s\n**Mumble users:** %s", discordList, mumbleList)

	resp := &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: content,
		},
	}
	_ = s.InteractionRespond(i, resp)
}