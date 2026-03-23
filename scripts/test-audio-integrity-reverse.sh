#!/bin/bash
# Audio Integrity Test (Reverse): TTS → discord-probe send → bridge → mumble-probe recv → STT
#
# This script tests audio integrity through the mumble-discord bridge in reverse direction:
# 1. Start Mumble server container
# 2. Start bridge binary
# 3. Start mumble-probe in receive mode
# 4. Send test audio via discord-probe
# 5. Verify STT transcription matches expected phrase
#
# Usage:
#   ./scripts/test-audio-integrity-reverse.sh [options]
#
# Options:
#   --phrase FILE      Audio file to send (default: phrase-001.wav)
#   --expect TEXT      Expected transcription text
#   --bridge PATH      Path to bridge binary (default: ./bridge)
#   --mumble-probe IMG Docker image for mumble-probe
#   --discord-probe IMG Docker image for discord-probe
#   --stt-endpoint URL STT service endpoint (default: http://localhost:5092/v1/audio/transcriptions)
#   --timeout SECS     Overall test timeout (default: 120)
#   --debug            Enable verbose output
#
# Exit codes:
#   0 = PASS (STT matched with ≥ 90% similarity)
#   1 = STT_MISMATCH (similarity < 90%)
#   2 = CONNECTION_ERROR
#   3 = TIMEOUT
#   4 = CONFIG_ERROR
#   5 = BRIDGE_ERROR
#   6 = MUMBLE_ERROR
#   7 = DISCORD_ERROR
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
MUMBLE_PROBE_IMG="stieneee/mumble-probe:latest"
DISCORD_PROBE_IMG="stieneee/discord-probe:latest"
STT_ENDPOINT="http://localhost:5092/v1/audio/transcriptions"
TIMEOUT_SECS=120
DEBUG=false

# Latency thresholds
LATENCY_WARN_MS=500
LATENCY_FAIL_MS=1000

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
# FUZZY MATCHING (Levenshtein Distance)
# ============================================================

# Normalize text: lowercase, strip punctuation, collapse whitespace
normalize_text() {
    echo "$1" | tr '[:upper:]' '[:lower:]' | tr -d '[:punct:]' | tr -s '[:space:]' | sed 's/^[[:space:]]*//;s/[[:space:]]*$//'
}

# Calculate Levenshtein distance between two strings
levenshtein_distance() {
    local s1="$1"
    local s2="$2"
    
    python3 -c "
import sys
s1, s2 = sys.argv[1], sys.argv[2]
m, n = len(s1), len(s2)
if m == 0:
    print(n)
    return
if n == 0:
    print(m)
    return
dp = [[0] * (n + 1) for _ in range(m + 1)]
for i in range(m + 1):
    dp[i][0] = i
for j in range(n + 1):
    dp[0][j] = j
for i in range(1, m + 1):
    for j in range(1, n + 1):
        if s1[i-1] == s2[j-1]:
            dp[i][j] = dp[i-1][j-1]
        else:
            dp[i][j] = 1 + min(dp[i-1][j], dp[i][j-1], dp[i-1][j-1])
print(dp[m][n])
" "$s1" "$s2"
}

# Calculate similarity score (0.0 to 1.0)
calculate_similarity() {
    local expected="$1"
    local actual="$2"
    
    local norm_expected
    local norm_actual
    norm_expected=$(normalize_text "$expected")
    norm_actual=$(normalize_text "$actual")
    
    if [[ -z "$norm_expected" ]] && [[ -z "$norm_actual" ]]; then
        echo "1.0"
        return
    fi
    if [[ -z "$norm_expected" ]] || [[ -z "$norm_actual" ]]; then
        echo "0.0"
        return
    fi
    
    local max_len=${#norm_expected}
    [[ ${#norm_actual} -gt $max_len ]] && max_len=${#norm_actual}
    
    local distance
    distance=$(levenshtein_distance "$norm_expected" "$norm_actual")
    
    local similarity
    similarity=$(python3 -c "print(round(1 - $distance / $max_len, 4))")
    echo "$similarity"
}

# Verify transcription with fuzzy matching
verify_transcription() {
    local expected="$1"
    local actual="$2"
    local min_similarity="${3:-0.9}"
    
    local norm_expected
    local norm_actual
    norm_expected=$(normalize_text "$expected")
    norm_actual=$(normalize_text "$actual")
    
    # Exact match (normalized)
    if [[ "$norm_expected" == "$norm_actual" ]]; then
        echo "PASS"
        return
    fi
    
    # Calculate similarity
    local similarity
    similarity=$(calculate_similarity "$expected" "$actual")
    
    local threshold
    threshold=$(python3 -c "print($similarity >= $min_similarity)")
    
    if [[ "$threshold" == "True" ]]; then
        echo "PASS"
    else
        echo "FAIL: $similarity"
    fi
}

# Generate JSON report
generate_json_report() {
    local test_id="$1"
    local phrase="$2"
    local expected="$3"
    local actual="$4"
    local similarity="$5"
    local result="$6"
    local latency_ms="$7"
    
    cat <<EOF
{
  "test_id": "$test_id",
  "direction": "discord-to-mumble",
  "phrase": "$phrase",
  "expected": "$expected",
  "actual": "$actual",
  "similarity": "$similarity",
  "result": "$result",
  "latency_ms": $latency_ms
}
EOF
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
        --mumble-probe)
            MUMBLE_PROBE_IMG="$2"
            shift 2
            ;;
        --discord-probe)
            DISCORD_PROBE_IMG="$2"
            shift 2
            ;;
        --stt-endpoint)
            STT_ENDPOINT="$2"
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
            head -30 "$0" | tail -28 | sed 's/^# //'
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
        EXPECTED_TEXT=$(python3 -c "
import json, sys
with open('$TEST_ASSETS/phrases.json') as f:
    data = json.load(f)
for p in data.get('phrases', []):
    if p.get('filename') == '$PHRASE_FILE':
        print(p.get('text', ''))
        break
" 2>/dev/null || echo "")
        
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
RECV_LOG=""
SEND_LOG=""

cleanup() {
    log_debug "Cleaning up..."
    
    # Stop mumble-probe receiver (if running)
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
        --name mumble-test-reverse-$$ \
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
# TEST EXECUTION
# ============================================================

run_mumble_receiver() {
    log_info "Starting mumble-probe receiver..."
    
    RECV_LOG=$(mktemp)
    SEND_LOG=$(mktemp)
    
    # Log recv_start timestamp
    log_timestamp_event "recv_start" "$RECV_LOG"
    
    docker run --rm \
        --network host \
        -v "$AUDIO_DIR:/audio:ro" \
        "$MUMBLE_PROBE_IMG" \
        --server "localhost:64738" \
        --username "audio-test-receiver" \
        --channel "Root" \
        --mode recv \
        --duration $((TIMEOUT_SECS - 30)) \
        --output /tmp/recording-mumble.wav \
        --stt \
        --expect "$EXPECTED_TEXT" \
        --stt-provider custom \
        --stt-endpoint "$STT_ENDPOINT" \
        > "$RECV_LOG" 2>&1 &
    
    RECV_PID=$!
    log_debug "Receiver PID: $RECV_PID"
    
    # Wait for receiver to be ready
    sleep 3
    log_success "Mumble receiver started"
}

send_discord_audio() {
    log_info "Sending audio via discord-probe..."
    log_info "Phrase: '$EXPECTED_TEXT'"
    
    # Log send_start timestamp
    log_timestamp_event "send_start" "$SEND_LOG"
    
    docker run --rm \
        --network host \
        -e DISCORD_TOKEN \
        -v "$AUDIO_PATH:/audio.wav:ro" \
        "$DISCORD_PROBE_IMG" \
        --token "$DISCORD_TOKEN" \
        --guild "$DISCORD_GUILD_ID" \
        --channel "$DISCORD_CHANNEL_ID" \
        --mode send \
        --audio-file /audio.wav
    
    local result=$?
    
    # Log send_end timestamp
    log_timestamp_event "send_end" "$SEND_LOG"
    
    if [ $result -eq 0 ]; then
        log_success "Audio sent successfully via Discord"
    else
        log_error "discord-probe failed with exit code $result"
        exit 7
    fi
}

wait_for_verification() {
    log_info "Waiting for STT verification..."
    
    local send_start_ms=""
    local send_end_ms=""
    local recv_start_ms=""
    local recv_end_ms=""
    
    # Extract timestamps from logs
    send_start_ms=$(grep '"send_start"' "$SEND_LOG" 2>/dev/null | sed 's/.*"ts_ms": *\([0-9]*\).*/\1/')
    send_end_ms=$(grep '"send_end"' "$SEND_LOG" 2>/dev/null | sed 's/.*"ts_ms": *\([0-9]*\).*/\1/')
    recv_start_ms=$(grep '"recv_start"' "$RECV_LOG" 2>/dev/null | sed 's/.*"ts_ms": *\([0-9]*\).*/\1/')
    
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
    
    # Log recv_end timestamp
    log_timestamp_event "recv_end" "$RECV_LOG"
    recv_end_ms=$(grep '"recv_end"' "$RECV_LOG" 2>/dev/null | sed 's/.*"ts_ms": *\([0-9]*\).*/\1/')
    
    # Get receiver exit code
    wait "$RECV_PID" 2>/dev/null
    local recv_result=$?
    
    log_debug "Receiver exit code: $recv_result"
    
    # Calculate latency if we have timestamps
    local latency_ms=0
    if [ -n "$send_start_ms" ] && [ -n "$recv_end_ms" ]; then
        latency_ms=$((recv_end_ms - send_start_ms))
        log_debug "End-to-end latency: ${latency_ms}ms"
    fi
    
    # Extract transcribed text from receiver log
    local transcribed=""
    if [ -f "$RECV_LOG" ]; then
        transcribed=$(grep -i "transcription\|transcribed\|stt.*result\|recognized" "$RECV_LOG" 2>/dev/null | head -1 || echo "")
        if [ -n "$transcribed" ]; then
            transcribed=$(echo "$transcribed" | sed 's/.*[:][[:space:]]*//')
        fi
    fi
    
    # If probe already did verification, use its result
    if grep -q "Verification PASSED" "$RECV_LOG" 2>/dev/null; then
        local similarity
        similarity=$(calculate_similarity "$EXPECTED_TEXT" "$transcribed")
        
        log_success "Audio integrity test PASSED"
        log_debug "Transcribed: $transcribed"
        
        generate_json_report "discord-to-mumble-${PHRASE_FILE%.wav}" \
            "$PHRASE_FILE" "$EXPECTED_TEXT" "$transcribed" "$similarity" "PASS" "$latency_ms"
        
        # Check latency thresholds
        if [ $latency_ms -gt $LATENCY_FAIL_MS ]; then
            log_error "Latency exceeds FAIL threshold (${latency_ms}ms > ${LATENCY_FAIL_MS}ms)"
            return 1
        elif [ $latency_ms -gt $LATENCY_WARN_MS ]; then
            log_warn "Latency exceeds WARN threshold (${latency_ms}ms > ${LATENCY_WARN_MS}ms)"
        fi
        
        return 0
    elif grep -q "Verification FAILED" "$RECV_LOG" 2>/dev/null; then
        local similarity="0.0"
        [ -n "$transcribed" ] && similarity=$(calculate_similarity "$EXPECTED_TEXT" "$transcribed")
        
        log_error "STT verification FAILED"
        
        generate_json_report "discord-to-mumble-${PHRASE_FILE%.wav}" \
            "$PHRASE_FILE" "$EXPECTED_TEXT" "$transcribed" "$similarity" "FAIL" "$latency_ms"
        
        return 1
    fi
    
    # If no probe result but we have transcribed text, do our own verification
    if [ -n "$transcribed" ]; then
        local verification
        verification=$(verify_transcription "$EXPECTED_TEXT" "$transcribed" "0.9")
        
        local similarity
        similarity=$(calculate_similarity "$EXPECTED_TEXT" "$transcribed")
        
        if [ "$verification" == "PASS" ]; then
            log_success "Audio integrity test PASSED (fuzzy match)"
            log_debug "Transcribed: $transcribed, Similarity: $similarity"
            
            generate_json_report "discord-to-mumble-${PHRASE_FILE%.wav}" \
                "$PHRASE_FILE" "$EXPECTED_TEXT" "$transcribed" "$similarity" "PASS" "$latency_ms"
            
            # Check latency thresholds
            if [ $latency_ms -gt $LATENCY_FAIL_MS ]; then
                log_error "Latency exceeds FAIL threshold (${latency_ms}ms > ${LATENCY_FAIL_MS}ms)"
                return 1
            elif [ $latency_ms -gt $LATENCY_WARN_MS ]; then
                log_warn "Latency exceeds WARN threshold (${latency_ms}ms > ${LATENCY_WARN_MS}ms)"
            fi
            
            return 0
        else
            log_error "STT verification FAILED (similarity: $similarity)"
            log_error "Expected: $EXPECTED_TEXT, Got: $transcribed"
            
            generate_json_report "discord-to-mumble-${PHRASE_FILE%.wav}" \
                "$PHRASE_FILE" "$EXPECTED_TEXT" "$transcribed" "$similarity" "FAIL" "$latency_ms"
            
            return 1
        fi
    fi
    
    # Timeout or crash
    log_error "Test timed out or receiver crashed"
    log_error "Receiver log:"
    cat "$RECV_LOG" >&2
    
    generate_json_report "discord-to-mumble-${PHRASE_FILE%.wav}" \
        "$PHRASE_FILE" "$EXPECTED_TEXT" "$transcribed" "0.0" "TIMEOUT" "$latency_ms"
    
    return 3
}

# ============================================================
# MAIN
# ============================================================

main() {
    log_info "========================================="
    log_info "Audio Integrity Test: Discord → Mumble"
    log_info "========================================="
    log_info "Phrase file: $PHRASE_FILE"
    log_info "Expected text: '$EXPECTED_TEXT'"
    log_info "Timeout: ${TIMEOUT_SECS}s"
    log_info "Latency thresholds: warn > ${LATENCY_WARN_MS}ms, fail > ${LATENCY_FAIL_MS}ms"
    log_info ""
    
    validate_env
    
    # Start infrastructure
    start_mumble_server
    start_bridge
    
    # Start receiver first (listening mode on Mumble)
    run_mumble_receiver
    
    # Send test audio via Discord
    send_discord_audio
    
    # Wait for verification
    local result
    wait_for_verification
    result=$?
    
    log_info "========================================="
    if [ $result -eq 0 ]; then
        log_success "Audio integrity test completed successfully"
    else
        log_error "Audio integrity test failed with code $result"
    fi
    log_info "========================================="
    
    exit $result
}

main "$@"
