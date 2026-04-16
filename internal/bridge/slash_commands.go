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

// respondEphemeral sends an ephemeral interaction response and logs any error.
func respondEphemeral(s *discordgo.Session, i *discordgo.InteractionCreate, content string) {
	resp := &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: content,
			Flags:   discordgo.MessageFlagsEphemeral,
		},
	}
	_ = s.InteractionRespond(i, resp)
}

// respond sends a public interaction response and logs any error.
func respond(s *discordgo.Session, i *discordgo.InteractionCreate, content string) {
	resp := &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: content,
		},
	}
	_ = s.InteractionRespond(i, resp)
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
		respondEphemeral(s, i, "You don't have permission to use this command.")
		return
	}

	// Check mode
	if b.Mode == BridgeModeConstant {
		respondEphemeral(s, i, "Constant mode enabled, connect command not available.")
		return
	}

	// Check if already connected
	b.BridgeMutex.Lock()
	connected := b.Connected
	b.BridgeMutex.Unlock()

	if connected {
		respondEphemeral(s, i, "Bridge already running.")
		return
	}

	// Start bridge
	b.Logger.Info("SLASH_COMMAND", fmt.Sprintf("User %s triggering bridge connect", username))
	go b.StartBridge()

	respond(s, i, "Bridge connecting...")
}

// handleSlashDisconnect handles the /disconnect command
func (b *BridgeState) handleSlashDisconnect(s *discordgo.Session, i *discordgo.InteractionCreate) {
	userID, username := interactionUser(i)

	// Check admin permission
	if !b.IsUserAdmin(userID, username) {
		b.Logger.Info("SLASH_COMMAND", fmt.Sprintf("User %s attempted /disconnect without admin permissions", username))
		respondEphemeral(s, i, "You don't have permission to use this command.")
		return
	}

	// Check mode
	if b.Mode == BridgeModeConstant {
		respondEphemeral(s, i, "Constant mode enabled, disconnect command not available.")
		return
	}

	// Check if not connected
	b.BridgeMutex.Lock()
	connected := b.Connected
	b.BridgeMutex.Unlock()

	if !connected {
		respondEphemeral(s, i, "Bridge is not running.")
		return
	}

	// Stop bridge
	b.Logger.Info("SLASH_COMMAND", fmt.Sprintf("User %s triggering bridge disconnect", username))
	go b.StopBridge()

	respond(s, i, "Bridge disconnecting...")
}

// handleSlashMode handles the /mode command
func (b *BridgeState) handleSlashMode(s *discordgo.Session, i *discordgo.InteractionCreate) {
	userID, username := interactionUser(i)

	// Check admin permission
	if !b.IsUserAdmin(userID, username) {
		b.Logger.Info("SLASH_COMMAND", fmt.Sprintf("User %s attempted /mode without admin permissions", username))
		respondEphemeral(s, i, "You don't have permission to use this command.")
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
		respondEphemeral(s, i, "Invalid mode specified.")
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
		respondEphemeral(s, i, "Invalid mode. Use auto, manual, or constant.")
		return
	}

	b.Logger.Info("SLASH_COMMAND", fmt.Sprintf("User %s changed mode from %s to %s", username, oldMode, b.Mode.String()))

	respond(s, i, fmt.Sprintf("Bridge mode changed from %s to %s", oldMode, b.Mode.String()))
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

	respond(s, i, content)
}