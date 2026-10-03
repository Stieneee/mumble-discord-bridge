#!/bin/bash
# Latency Measurement Test: Timestamp audio send/recv, measure round-trip delay
#
# This script extends the audio integrity test with latency measurement:
# 1. Start Mumble server container
# 2. Start bridge binary  
# 3. Start discord-probe in receive mode with timestamp logging
# 4. Send test audio via mumble-probe with timestamp logging
# 5. Calculate latency metrics and compare against thresholds
#
# Usage:
#   ./scripts/test-latency.sh [options]
#
# Options:
#   --phrase FILE      Audio file to send (default: phrase-001.wav)
#   --expect TEXT      Expected transcription text
#   --bridge PATH      Path to bridge binary (default: ./bridge)
#   --output JSON      Output JSON file for results (default: /tmp/latency-result.json)
#   --timeout SECS     Overall test timeout (default: 120)
#   --debug            Enable verbose output
#
# Output (JSON):
#   {
#     "test_id": "latency-001",
#     "phrase": "phrase-001.wav",
#     "timestamps": {
#       "send_start_ms": 1709856000000,
#       "send_end_ms": 1709856001200,
#       "recv_start_ms": 1709856001220,
#       "recv_end_ms": 1709856002400
#     },
#     "latency": {
#       "end_to_end_ms": 2400,
#       "bridge_ms": 20,
#       "network_send_ms": 1200,
#       "network_recv_ms": 1180
#     },
#     "result": "PASS|WARN|FAIL",
#     "thresholds": {
#       "target_ms": 300,
#       "warn_ms": 500,
#       "fail_ms": 1000
#     }
#   }
#
# Exit codes:
#   0 = PASS (latency under warn threshold)
#   1 = FAIL (latency exceeds fail threshold)
#   2 = WARN (latency exceeds warn but under fail threshold)
#   3 = TIMEOUT
#   4 = CONFIG_ERROR
#   5 = BRIDGE_ERROR
#   6 = MUMBLE_ERROR
#   7 = DISCORD_ERROR
#   8 = STT_ERROR
#
# Environment variables:
#   DISCORD_TOKEN      Discord bot token (required)
#   DISCORD_GUILD_ID   Discord guild ID (required)
#   DISCORD_CHANNEL_ID Discord voice channel ID (required)
#   MUMBLE_PASSWORD    Mumble server password (optional)
#
# ============================================================

set -e

# ============================================================
# CONFIGURATION
# ============================================================

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BRIDGE_ROOT="$(dirname "$SCRIPT_DIR")"
TEST_ASSETS="$BRIDGE_ROOT/scripts/test-assets"
AUDIO_DIR="$TEST_ASSETS/audio"

# Default values
PHRASE_FILE="phrase-001.wav"
EXPECTED_TEXT=""
BRIDGE_BIN="./bridge"
OUTPUT_JSON="/tmp/latency-result.json"
TIMEOUT_SECS=120
DEBUG=false

# Latency thresholds
TARGET_MS=300
WARN_MS=500
FAIL_MS=1000

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# ============================================================
# LOGGING
# ============================================================

log_info() {
    echo -e "${BLUE}[INFO]${NC} $1"
}

log_success() {
    echo -e "${GREEN}[PASS]${NC} $1"
}

log_error() {
    echo -e "${RED}[FAIL]${NC} $1"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

log_debug() {
    if [ "$DEBUG" = true ]; then
        echo -e "${YELLOW}[DEBUG]${NC} $1"
    fi
}

# ============================================================
# TIMESTAMP FUNCTIONS (milliseconds since epoch)
# ============================================================

get_timestamp_ms() {
    date +%s%3N
}

log_timestamp_event() {
    local event="$1"
    local ts_ms=$(get_timestamp_ms)
    local log_file="$2"
    echo "{\"event\": \"$event\", \"ts_ms\": $ts_ms}" >> "$log_file"
    log_debug "Timestamp: $event = $ts_ms"
}

# ============================================================
# PARSE ARGUMENTS
# ============================================================

while [[ $# -gt 0 ]]; do
    case $1 in
        --phrase)
            PHRASE_FILE="$2"
            shift 2
            ;;
        --expect)
            EXPECTED_TEXT="$2"
            shift 2
            ;;
        --bridge)
            BRIDGE_BIN="$2"
            shift 2
            ;;
        --output)
            OUTPUT_JSON="$2"
            shift 2
            ;;
        --timeout)
            TIMEOUT_SECS="$2"
            shift 2
            ;;
        --debug)
            DEBUG=true
            shift
            ;;
        -h|--help)
            head -50 "$0" | tail -45 | sed 's/^# //'
            exit 0
            ;;
        *)
            log_error "Unknown option: $1"
            exit 4
            ;;
    esac
done

# ============================================================
# VALIDATE ENVIRONMENT
# ============================================================

validate_env() {
    local missing=()
    
    if [ -z "$DISCORD_TOKEN" ]; then
        missing+=("DISCORD_TOKEN")
    fi
    if [ -z "$DISCORD_GUILD_ID" ]; then
        missing+=("DISCORD_GUILD_ID")
    fi
    if [ -z "$DISCORD_CHANNEL_ID" ]; then
        missing+=("DISCORD_CHANNEL_ID")
    fi
    
    if [ ${#missing[@]} -gt 0 ]; then
        log_error "Missing required environment variables: ${missing[*]}"
        exit 4
    fi
    
    # Check phrase file exists
    if [ -f "$AUDIO_DIR/$PHRASE_FILE" ]; then
        AUDIO_PATH="$AUDIO_DIR/$PHRASE_FILE"
        log_debug "Using phrase file: $AUDIO_PATH"
    elif [ -f "$PHRASE_FILE" ]; then
        AUDIO_PATH="$PHRASE_FILE"
        log_debug "Using phrase file: $AUDIO_PATH (absolute path)"
    else
        log_error "Phrase file not found: $PHRASE_FILE"
        exit 4
    fi
    
    # Get expected text from phrases.json if not provided
    if [ -z "$EXPECTED_TEXT" ] && [ -f "$TEST_ASSETS/phrases.json" ]; then
        EXPECTED_TEXT=$(grep -A1 "\"$PHRASE_FILE\"" "$TEST_ASSETS/phrases.json" | grep '"canonical"' | sed 's/.*"canonical": *"\([^"]*\)".*/\1/')
        if [ -n "$EXPECTED_TEXT" ]; then
            log_debug "Auto-detected expected text: $EXPECTED_TEXT"
        fi
    fi
    
    if [ -z "$EXPECTED_TEXT" ]; then
        log_error "Expected text not provided. Use --expect or ensure phrases.json exists."
        exit 4
    fi
}

# ============================================================
# CONTAINER MANAGEMENT
# ============================================================

MUMBLE_CONTAINER=""
BRIDGE_PID=""
RECV_PID=""
SEND_LOG=""
RECV_LOG=""

cleanup() {
    log_debug "Cleaning up..."
    
    # Stop discord-probe receiver (if running)
    if [ -n "$RECV_PID" ] && kill -0 "$RECV_PID" 2>/dev/null; then
        kill "$RECV_PID" 2>/dev/null || true
    fi
    
    # Stop bridge
    if [ -n "$BRIDGE_PID" ] && kill -0 "$BRIDGE_PID" 2>/dev/null; then
        kill "$BRIDGE_PID" 2>/dev/null || true
        wait "$BRIDGE_PID" 2>/dev/null || true
    fi
    
    # Stop Mumble container
    if [ -n "$MUMBLE_CONTAINER" ]; then
        docker stop "$MUMBLE_CONTAINER" 2>/dev/null || true
        docker rm "$MUMBLE_CONTAINER" 2>/dev/null || true
    fi
    
    log_debug "Cleanup complete"
}

trap cleanup EXIT

start_mumble_server() {
    log_info "Starting Mumble server..."
    
    MUMBLE_CONTAINER=$(docker run -d \
        --name mumble-latency-test-$$ \
        -p 64738:64738/tcp \
        -p 64738:64738/udp \
        -e MUMBLE_CONFIG="port=64738
users=50
bandwidth=128000
icesecretread=
icesecretwrite=" \
        mumblevoip/mumble-server:latest)
    
    if [ -z "$MUMBLE_CONTAINER" ]; then
        log_error "Failed to start Mumble container"
        exit 6
    fi
    
    log_debug "Mumble container: $MUMBLE_CONTAINER"
    
    # Wait for Mumble to be ready
    log_info "Waiting for Mumble server to be ready..."
    local max_attempts=30
    local attempt=0
    while [ $attempt -lt $max_attempts ]; do
        if docker logs "$MUMBLE_CONTAINER" 2>&1 | grep -q "Server listening on"; then
            log_success "Mumble server is ready"
            return 0
        fi
        attempt=$((attempt + 1))
        sleep 1
    done
    
    log_error "Mumble server failed to start within ${max_attempts}s"
    docker logs "$MUMBLE_CONTAINER"
    exit 6
}

start_bridge() {
    log_info "Starting bridge..."
    
    if [ -x "$BRIDGE_BIN" ]; then
        "$BRIDGE_BIN" &
        BRIDGE_PID=$!
        log_debug "Bridge PID: $BRIDGE_PID"
        sleep 3
        log_success "Bridge started"
    else
        log_debug "Bridge binary not found at $BRIDGE_BIN, assuming external bridge"
    fi
}

# ============================================================
# TEST EXECUTION WITH TIMESTAMPS
# ============================================================

run_discord_receiver() {
    log_info "Starting discord-probe receiver with timestamp logging..."
    
    RECV_LOG=$(mktemp)
    SEND_LOG=$(mktemp)
    
    # Receiver starts listening - log recv_start
    log_timestamp_event "recv_start" "$RECV_LOG"
    
    docker run --rm \
        --network host \
        -e DISCORD_TOKEN \
        -v "$AUDIO_DIR:/audio:ro" \
        "stieneee/discord-probe:latest" \
        --token "$DISCORD_TOKEN" \
        --guild "$DISCORD_GUILD_ID" \
        --channel "$DISCORD_CHANNEL_ID" \
        --mode recv \
        --duration $((TIMEOUT_SECS - 30)) \
        --output /tmp/recording.wav \
        --stt \
        --expect "$EXPECTED_TEXT" \
        --stt-provider custom \
        --stt-endpoint "http://localhost:5092/v1/audio/transcriptions" \
        > "$RECV_LOG" 2>&1 &
    
    RECV_PID=$!
    log_debug "Receiver PID: $RECV_PID"
    
    # Wait for receiver to be ready
    sleep 3
    log_success "Discord receiver started"
}

send_mumble_audio() {
    log_info "Sending audio via mumble-probe with timestamp logging..."
    log_info "Phrase: '$EXPECTED_TEXT'"
    
    # Log send_start before starting
    log_timestamp_event "send_start" "$SEND_LOG"
    
    docker run --rm \
        --network host \
        -v "$AUDIO_PATH:/audio.wav:ro" \
        "stieneee/mumble-probe:latest" \
        --server "localhost:64738" \
        --username "latency-test-sender" \
        --channel "Root" \
        --mode send \
        --audio-file /audio.wav
    
    local result=$?
    
    # Log send_end after completion
    log_timestamp_event "send_end" "$SEND_LOG"
    
    if [ $result -eq 0 ]; then
        log_success "Audio sent successfully"
    else
        log_error "mumble-probe failed with exit code $result"
        exit 6
    fi
}

wait_for_completion() {
    log_info "Waiting for test completion..."
    
    # Wait for receiver to complete (with timeout)
    local wait_time=0
    local max_wait=$((TIMEOUT_SECS - 30))
    
    while [ $wait_time -lt $max_wait ]; do
        if ! kill -0 "$RECV_PID" 2>/dev/null; then
            break
        fi
        sleep 1
        wait_time=$((wait_time + 1))
    done
    
    # Get receiver exit code
    wait "$RECV_PID" 2>/dev/null
    local recv_result=$?
    
    # Log recv_end after receiver finishes
    log_timestamp_event "recv_end" "$RECV_LOG"
    
    log_debug "Receiver exit code: $recv_result"
    
    return $recv_result
}

# ============================================================
# LATENCY CALCULATION
# ============================================================

parse_timestamps() {
    local send_start=$(grep '"send_start"' "$SEND_LOG" | sed 's/.*"ts_ms": *\([0-9]*\).*/\1/')
    local send_end=$(grep '"send_end"' "$SEND_LOG" | sed 's/.*"ts_ms": *\([0-9]*\).*/\1/')
    local recv_start=$(grep '"recv_start"' "$RECV_LOG" | sed 's/.*"ts_ms": *\([0-9]*\).*/\1/')
    local recv_end=$(grep '"recv_end"' "$RECV_LOG" | sed 's/.*"ts_ms": *\([0-9]*\).*/\1/')
    
    # Handle missing timestamps gracefully
    if [ -z "$send_start" ]; then
        log_error "Missing send_start timestamp"
        return 1
    fi
    if [ -z "$send_end" ]; then
        log_error "Missing send_end timestamp"
        return 1
    fi
    if [ -z "$recv_start" ]; then
        log_error "Missing recv_start timestamp"
        return 1
    fi
    if [ -z "$recv_end" ]; then
        log_error "Missing recv_end timestamp"
        return 1
    fi
    
    # Calculate latencies
    local end_to_end_ms=$((recv_end - send_start))
    local bridge_ms=$((recv_start - send_end))
    local network_send_ms=$((send_end - send_start))
    local network_recv_ms=$((recv_end - recv_start))
    
    # Determine result based on thresholds
    local result="PASS"
    local exit_code=0
    
    if [ $end_to_end_ms -gt $FAIL_MS ]; then
        result="FAIL"
        exit_code=1
    elif [ $end_to_end_ms -gt $WARN_MS ]; then
        result="WARN"
        exit_code=2
    fi
    
    # Generate JSON output
    cat > "$OUTPUT_JSON" << EOF
{
  "test_id": "latency-$(date +%Y%m%d%H%M%S)",
  "phrase": "$PHRASE_FILE",
  "timestamps": {
    "send_start_ms": $send_start,
    "send_end_ms": $send_end,
    "recv_start_ms": $recv_start,
    "recv_end_ms": $recv_end
  },
  "latency": {
    "end_to_end_ms": $end_to_end_ms,
    "bridge_ms": $bridge_ms,
    "network_send_ms": $network_send_ms,
    "network_recv_ms": $network_recv_ms
  },
  "result": "$result",
  "thresholds": {
    "target_ms": $TARGET_MS,
    "warn_ms": $WARN_MS,
    "fail_ms": $FAIL_MS
  }
}
EOF
    
    echo "$exit_code|$end_to_end_ms|$result"
}

# ============================================================
# MAIN
# ============================================================

main() {
    log_info "========================================="
    log_info "Latency Measurement Test"
    log_info "========================================="
    log_info "Phrase file: $PHRASE_FILE"
    log_info "Expected text: '$EXPECTED_TEXT'"
    log_info "Thresholds: target < ${TARGET_MS}ms, warn > ${WARN_MS}ms, fail > ${FAIL_MS}ms"
    log_info "Timeout: ${TIMEOUT_SECS}s"
    log_info ""
    
    validate_env
    
    # Start infrastructure
    start_mumble_server
    start_bridge
    
    # Start receiver first (listening mode)
    run_discord_receiver
    
    # Send test audio
    send_mumble_audio
    
    # Wait for completion
    wait_for_completion
    local wait_result=$?
    
    # Calculate latency
    local latency_result
    latency_result=$(parse_timestamps)
    local exit_code=${latency_result%%|*}
    local end_to_end=${latency_result##*|}
    local result_name=${latency_result##*|}
    result_name=$(echo "$latency_result" | cut -d'|' -f3)
    
    log_info "========================================="
    
    # Display results
    if [ -f "$OUTPUT_JSON" ]; then
        log_info "Results:"
        cat "$OUTPUT_JSON" | python3 -m json.tool 2>/dev/null || cat "$OUTPUT_JSON"
        log_info ""
    fi
    
    case "$result_name" in
        PASS)
            log_success "Latency test PASSED (${end_to_end}ms < ${WARN_MS}ms)"
            ;;
        WARN)
            log_warn "Latency test WARNING (${end_to_end}ms > ${WARN_MS}ms)"
            ;;
        FAIL)
            log_error "Latency test FAILED (${end_to_end}ms > ${FAIL_MS}ms)"
            ;;
    esac
    
    log_info "========================================="
    
    # Return exit code: 0=PASS, 1=FAIL, 2=WARN
    exit $exit_code
}

main "$@"
