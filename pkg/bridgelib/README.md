# BridgeLib Package

The BridgeLib package provides a modular, reusable implementation of the Mumble-Discord bridge functionality, enabling multiple bridge instances to share a single Discord client connection.

## Overview

BridgeLib was created to address a critical limitation in the original mumble-discord-bridge architecture where each bridge process required its own Discord bot connection. This caused issues where Discord messages would only be delivered to one bridge instance when multiple bridges were running.

By implementing a shared Discord client with intelligent message routing, BridgeLib solves this problem and enables more efficient operation of multiple bridge instances.

## Key Features

- **Shared Discord Client**: Single connection to Discord for all bridge instances
- **Message Routing**: Proper routing of Discord events to the appropriate bridge
- **Bridge Abstraction**: Clean interface for bridge lifecycle management
- **Consistent Behavior**: Same behavior in both single and multi-bridge deployments
- **Resource Efficiency**: Reduced memory and connection overhead

## Usage

### Single Bridge Mode

```go
package main

import (
    "log"
    "github.com/stieneee/mumble-discord-bridge/pkg/bridgelib"
)

func main() {
    // Create shared Discord client (nil uses default console logger)
    discordClient, err := bridgelib.NewSharedDiscordClient("YOUR_DISCORD_BOT_TOKEN", nil)
    if err != nil {
        log.Fatalln("Failed to create Discord client:", err)
    }
    
    // Connect to Discord
    err = discordClient.Connect()
    if err != nil {
        log.Fatalln("Failed to connect to Discord:", err)
    }
    defer discordClient.Disconnect()
    
    // Create bridge configuration
    config := &bridgelib.BridgeConfig{
        Command:                 "mumble-discord",
        MumbleAddress:           "mumble.example.com",
        MumblePort:              64738,
        MumbleUsername:          "DiscordBridge",
        MumblePassword:          "password",
        MumbleChannel:           "General",
        MumbleInsecure:          false,
        MumbleCertificate:       "",
        DiscordGID:      "YOUR_GUILD_ID",
        DiscordCID:      "YOUR_CHANNEL_ID",
        DiscordTextMode: "channel",
        ChatBridge:      true,
        Mode:                    "constant",
    }
    
    // Create and start bridge instance
    bridgeInstance, err := bridgelib.NewBridgeInstance("default", config, discordClient)
    if err != nil {
        log.Fatalln("Failed to create bridge instance:", err)
    }
    
    // Start the bridge
    if err := bridgeInstance.Start(); err != nil {
        log.Fatalln("Failed to start bridge:", err)
    }
    
    // Wait for termination signal
    // ...
    
    // Stop the bridge
    if err := bridgeInstance.Stop(); err != nil {
        log.Println("Error stopping bridge:", err)
    }
}
```

### Multi-Bridge Mode

```go
// Create multiple bridge instances with the same Discord client
for id, bridgeConfig := range bridges {
    config := &bridgelib.BridgeConfig{
        // Configure bridge settings
        DiscordGID: bridgeConfig.GuildID,
        DiscordCID: bridgeConfig.ChannelID,
        // ... other settings
    }
    
    bridge, err := bridgelib.NewBridgeInstance(id, config, sharedDiscordClient)
    if err != nil {
        log.Printf("Error creating bridge %s: %v", id, err)
        continue
    }
    
    if err := bridge.Start(); err != nil {
        log.Printf("Error starting bridge %s: %v", id, err)
        continue
    }
    
    // Store bridge for later management
    bridgeInstances[id] = bridge
}
```

## Components

### BridgeInstance

The `BridgeInstance` represents a single bridge connection between Discord and Mumble:

```go
type BridgeInstance struct {
    // The internal bridge state
    State *bridge.BridgeState

    // The Discord provider
    discordProvider DiscordProvider

    // Bridge configuration
    config *BridgeConfig

    // Instance ID
    ID string
    
    // Other implementation details...
}
```

### SharedDiscordClient

The `SharedDiscordClient` implements the `DiscordProvider` interface to provide a centralized Discord client:

```go
type SharedDiscordClient struct {
    // The Discord session
    session *discordgo.Session

    // Mapping of guild:channel to message handlers
    messageHandlers     map[string][]interface{}
    messageHandlerMutex sync.RWMutex

}
```

## Message Routing

The message routing system is a key feature of BridgeLib. It works by:

1. Registering message handlers with a specific Guild ID and Channel ID
2. Routing incoming Discord messages to the appropriate handler based on the source
3. Handling cases where the Discord state cache is incomplete
4. Supporting fallback to direct API calls when necessary

This ensures that each bridge only processes messages from its configured Discord channel.

## Architectural Role of BridgeLib

BridgeLib serves as the critical intermediary layer between Discord and Mumble:

### Key Positioning
- **Discord Side**: Receives messages/audio from the Shared Discord Client
- **Processing Layer**: Handles protocol translation, message routing, and bridge logic
- **Mumble Side**: Manages individual connections to Mumble servers

### Benefits of This Architecture
1. **Protocol Abstraction**: BridgeLib handles the differences between Discord and Mumble protocols
2. **Intelligent Routing**: Each Discord channel is properly routed to its corresponding Mumble server
3. **Resource Efficiency**: Single Discord connection serves multiple Mumble connections
4. **Isolated Bridge Logic**: Each BridgeLib instance manages its own bridge state independently
5. **Scalability**: Easy to add/remove bridge instances without affecting others

## Bridge Lifecycle

BridgeLib provides a complete lifecycle management API:

1. **Creation**: `NewBridgeInstance` creates a new bridge instance
2. **Starting**: `Start` begins bridge operation based on the configured mode
3. **Running**: Bridge automatically processes events
4. **Stopping**: `Stop` gracefully terminates the bridge
5. **Status**: `GetStatus` provides the current bridge status

## Error Handling

BridgeLib implements robust error handling:

1. **Input Validation**: Configuration is validated before use
2. **Operation Errors**: Methods return errors for caller handling
3. **Runtime Recovery**: Critical sections include panic recovery
4. **Logging**: Comprehensive logging for diagnostics
5. **Graceful Degradation**: Continue operation when possible

## Architecture Diagrams

### Single Bridge Architecture

```
┌─────────────┐     ┌─────────────┐     ┌─────────────┐     ┌──────────┐
│   Discord   │<───>│   Shared    │<───>│ BridgeLib   │<───>│  Mumble  │
│   Channel   │     │  Discord    │     │  Instance   │     │  Server  │
│             │     │   Client    │     │             │     │          │
└─────────────┘     └─────────────┘     └─────────────┘     └──────────┘
```

### Multi-Bridge Architecture

```
┌─────────────┐     ┌─────────────┐     ┌─────────────┐     ┌──────────┐
│  Discord    │<───>│             │<───>│ BridgeLib   │<───>│  Mumble  │
│ Channel 1   │     │             │     │ Instance 1  │     │ Server 1 │
└─────────────┘     │             │     └─────────────┘     └──────────┘
                    │             │
┌─────────────┐     │   Shared    │     ┌─────────────┐     ┌──────────┐
│  Discord    │<───>│  Discord    │<───>│ BridgeLib   │<───>│  Mumble  │
│ Channel 2   │     │   Client    │     │ Instance 2  │     │ Server 2 │
└─────────────┘     │             │     └─────────────┘     └──────────┘
                    │             │
┌─────────────┐     │             │     ┌─────────────┐     ┌──────────┐
│  Discord    │<───>│             │<───>│ BridgeLib   │<───>│  Mumble  │
│ Channel 3   │     │             │     │ Instance 3  │     │ Server 3 │
└─────────────┘     └─────────────┘     └─────────────┘     └──────────┘
```

## Discord to Mumble Message Flow

```
Discord Channel → Shared Discord Client → Message Router → BridgeLib Instance → Mumble Server
```

## Mumble to Discord Message Flow

```
Mumble Server → BridgeLib Instance → Shared Discord Client → Discord Channel
```

## Multi-Instance Message Routing

```
Discord Channel 1 ──┐
Discord Channel 2 ──┼──> Shared Discord Client ──┬──> BridgeLib Instance 1 ──> Mumble Server 1
Discord Channel 3 ──┘      (Message Router)      ├──> BridgeLib Instance 2 ──> Mumble Server 2
                                                  └──> BridgeLib Instance 3 ──> Mumble Server 3
```

## Configuration Options

The `BridgeConfig` structure provides comprehensive configuration:

| Field                    | Description                                        |
|--------------------------|----------------------------------------------------|
| Command                  | Command prefix for bot control                     |
| MumbleAddress            | Mumble server address                             |
| MumblePort               | Mumble server port                                |
| MumbleUsername           | Username for Mumble connection                     |
| MumblePassword           | Password for Mumble connection                     |
| MumbleChannel            | Channel path to join                              |
| MumbleInsecure           | Skip TLS verification for Mumble                   |
| MumbleCertificate        | Client certificate for Mumble                      |
| MumbleSendBuffer         | Jitter buffer size for Discord→Mumble audio        |
| MumbleDisableText        | Disable text messages to Mumble                    |
| MumbleCommand            | Enable command processing in Mumble                |
| MumbleBotFlag            | Set bot flag in Mumble (v1.5+)                     |
| DiscordGID               | Discord Guild ID                                   |
| DiscordCID               | Discord Channel ID                                 |
| DiscordSendBuffer        | Jitter buffer size for Mumble→Discord audio        |
| DiscordTextMode          | Text mode (channel, user, disabled)                |
| DiscordCommand           | Enable command processing in Discord               |
| ChatBridge               | Enable text chat bridging                          |
| Mode                     | Bridge mode (constant, manual, auto)               |
| Version                  | Bridge version                                     |

## Performance Considerations

BridgeLib is designed for efficient operation:

1. **Memory Usage**: Shared client reduces memory overhead
2. **Connection Efficiency**: Single Discord connection for all bridges
3. **Message Routing**: O(1) lookups for message routing
4. **Resource Sharing**: Voice connections shared when possible
5. **Concurrency**: Fine-grained locking to minimize contention

## Future Improvements

1. **Message Filtering**: Support for configurable message filters
2. **Improved Voice Quality**: Enhanced audio processing options
3. **User Mapping**: Associate Discord users with Mumble users for better attribution
4. **Extended Metrics**: More detailed operational metrics
5. **Plugin System**: Support for custom functionality extensions

## Interface Implementations

BridgeLib uses a provider pattern to abstract Discord connectivity:

```go
// DiscordProvider interface
type DiscordProvider interface {
    // RegisterHandler registers a handler for Discord events
    RegisterHandler(handlerFunc interface{})

    // SendMessage sends a message to a channel
    SendMessage(channelID, content string) (*discordgo.Message, error)

    // GetSession returns the underlying Discord session
    GetSession() *discordgo.Session
}
```

This interface can be implemented by different Discord client implementations, enabling testing and flexibility.