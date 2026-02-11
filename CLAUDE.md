# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Mumble Discord Bridge is a Go application that bridges audio and text chat between Mumble and Discord voice channels. It allows users on both platforms to communicate seamlessly.

## Build & Development Commands

```bash
# Build (requires libopus-dev installed)
make                    # Full build with goreleaser
make dev                # Build and run for development

# Testing
go test -race -v ./...                              # Run all tests with race detector
go test -race -count=10 ./internal/bridge ./pkg/bridgelib  # Stress test (CI runs this)
go test -race -coverprofile=coverage.out ./internal/bridge ./pkg/bridgelib  # With coverage

# Linting
make lint               # Run golangci-lint
make format             # Format code with go fmt

# Run with race detector (development)
make dev-race           # go run -race ./cmd/mumble-discord-bridge

# Clean build artifacts
make clean
```

**System dependency**: Install `libopus-dev` (Ubuntu) or equivalent before building.

## Architecture

### Package Structure

- **cmd/mumble-discord-bridge**: CLI entry point, configuration parsing, signal handling
- **internal/bridge**: Core bridge logic
- **pkg/bridgelib**: High-level API for multi-bridge deployments (used by patchcord.io)
- **pkg/logger**: Logger abstraction (supports bridge-specific logging)
- **pkg/sleepct**: Precision sleep utility for audio timing

### Core Components (internal/bridge)

**BridgeState** (`bridge.go`): Central state holder managing:
- Connection states (Discord, Mumble, overall)
- User tracking maps with dedicated mutexes
- Audio stream lifecycle
- Three operational modes: `auto`, `manual`, `constant`

**Concurrency Model**: Uses multiple mutexes with strict lock ordering:
```
BridgeMutex -> MumbleUsersMutex -> DiscordUsersMutex
```
Always acquire in this order to prevent deadlocks.

**Connection Managers** (`connection_manager.go`, `discord_connection_manager.go`, `mumble_connection_manager.go`):
- Implement resilient connections with automatic reconnection
- Emit ConnectionEvents through channels
- BaseConnectionManager provides common retry/health check logic

**Audio Duplex** (`discord_duplex.go`, `mumble_duplex.go`):
- Handle bidirectional audio streaming
- Use jitter buffers (configurable via TO_DISCORD_BUFFER/TO_MUMBLE_BUFFER)
- MumbleDuplex mixes multiple Mumble audio streams

**Event Handlers** (`discord_handlers.go`, `mumble_handlers.go`):
- DiscordListener: Handles voice state updates, message creation
- MumbleListener: Handles user changes, text messages, connection events

### BridgeLib (pkg/bridgelib)

**SharedDiscordClient** (`discord.go`): Allows multiple bridge instances to share a single Discord session with message routing per guild/channel.

**BridgeInstance** (`bridge.go`): High-level wrapper that:
- Creates and manages internal BridgeState
- Handles mode-specific startup (auto/manual/constant)
- Provides EventDispatcher for external event consumption

**EventDispatcher** (`events.go`): Async event system with typed events (connection, user join/leave, bridge lifecycle).

### Bridge Modes

- **constant**: Always connected, auto-restarts on disconnect
- **auto**: Connects when users present on both sides, disconnects when empty
- **manual**: Controlled via chat commands (`!mumble-discord link/unlink`)

### discordgo Fork

The project uses a fork of `bwmarrin/discordgo` at `github.com/Stieneee/discordgo`. The fork lives at `../discordgo` relative to this repo (for local development, use `replace github.com/bwmarrin/discordgo => ../discordgo` in go.mod).

Key branches:

- **`mumble-discord-bridge`**: Stacked branch combining all MDB-related fixes (use this for go.mod replace in CI)
- **`fix/voice-aead-data-race`**: RLock snapshot for v.aead in opusSender/opusReceiver
- **`fix/session-open-deadlock-v2`**: Guard Open() inline onEvent against Op 7/9 deadlock
- **`fix/voice-rtp-silence-gap`**: Advance RTP timestamp across silence gaps + set marker bit

When adding a new discordgo fix: create an independent branch from `upstream/master`, cherry-pick it onto `mumble-discord-bridge`, push both, update go.mod replace.

### Audio Architecture

See `docs/AUDIO-PACKET-FLOW.md` for detailed audio pipeline documentation including packet flow diagrams, buffer sizing, and metrics reference.

Key audio concepts:

- **RTP timestamp must track wall clock** across silence gaps, otherwise Discord's adaptive jitter buffer grows monotonically
- **RTP marker bit (RFC 3551)** signals start of new talk-spurt after silence, allowing receivers to reset jitter buffer timing
- **Buffer depth cap** (`mumbleMaxBufferDepth`) prevents Mumble clock drift from accumulating latency
- Mumble uses 10ms frames, Discord uses 20ms Opus frames; the bridge converts between them

## Testing Patterns

Tests use mocks defined in `internal/bridge/mocks_test.go`. Common test utilities in `testutil_test.go`.

Race detection is critical - always run tests with `-race` flag as the codebase has complex concurrent state management.

## Configuration

All options support CLI flags, environment variables, or `.env` file. Required variables:
- MUMBLE_ADDRESS, DISCORD_TOKEN, DISCORD_GID, DISCORD_CID

See README.md for full configuration reference.
