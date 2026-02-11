package bridge

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	// Bridge General

	// PromApplicationStartTime tracks when the application started.
	PromApplicationStartTime = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "mdb_bridge_start_time",
		Help: "The time the application started",
	})

	promBridgeStarts = promauto.NewCounter(prometheus.CounterOpts{
		Name: "mdb_bridge_starts_count",
		Help: "The number of times the bridge start routine has been called",
	})

	promBridgeStartTime = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "mdb_bridge_starts_time",
		Help: "The time the current bridge instance started",
	})

	// MUMBLE
	promMumblePing = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "mdb_mumble_ping",
		Help: "Mumble ping",
	})

	promMumbleUsers = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "mdb_mumble_users_gauge",
		Help: "The number of connected Mumble users",
	})

	promReceivedMumblePackets = promauto.NewCounter(prometheus.CounterOpts{
		Name: "mdb_mumble_received_count",
		Help: "The count of Mumble audio packets received",
	})

	promSentMumblePackets = promauto.NewCounter(prometheus.CounterOpts{
		Name: "mdb_mumble_sent_count",
		Help: "The count of audio packets sent to mumble",
	})

	promMumbleBufferedPackets = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "mdb_to_mumble_buffer_gauge",
		Help: "The buffer size for packets to Mumble",
	})

	// promToMumbleDropped is kept for backward compatibility with existing dashboards.
	// promMumbleSendTimeouts tracks the same timeout events with clearer naming.
	promToMumbleDropped = promauto.NewCounter(prometheus.CounterOpts{
		Name: "mdb_to_mumble_dropped",
		Help: "The number of packets timeouts to mumble",
	})

	promMumbleSendTimeouts = promauto.NewCounter(prometheus.CounterOpts{
		Name: "mdb_mumble_send_timeouts_total",
		Help: "Number of timeouts when sending to gumble channel (packets dropped)",
	})

	promMumbleArraySize = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "mdb_to_mumble_array_size_gauge",
		Help: "The array size of mumble streams",
	})

	promMumbleStreaming = promauto.NewGauge(prometheus.GaugeOpts{ // SUMMARY?
		Name: "mdb_mumble_streaming_gauge",
		Help: "The number of active audio streams streaming audio from mumble",
	})

	promMumbleChunksSkipped = promauto.NewCounter(prometheus.CounterOpts{
		Name: "mdb_mumble_chunks_skipped_total",
		Help: "Number of stale audio chunks skipped due to buffer depth exceeding threshold (indicates clock drift)",
	})

	promMumbleMaxStreamDepth = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "mdb_mumble_max_stream_depth_gauge",
		Help: "Maximum buffer depth across all active Mumble audio streams (diagnostic for latency investigation)",
	})

	// DISCORD

	promSpeakingTransitions = promauto.NewCounter(prometheus.CounterOpts{
		Name: "mdb_discord_speaking_transitions_total",
		Help: "Number of silence-to-speaking transitions on Mumble-to-Discord path (each transition may grow Discord jitter buffer)",
	})

	promSilenceGapMs = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "mdb_discord_silence_gap_ms",
		Help:    "Duration of silence gaps before speaking resumes on Mumble-to-Discord path (milliseconds)",
		Buckets: []float64{50, 100, 500, 1000, 2000, 5000, 10000, 30000, 60000},
	})

	promRtpTimestampDrift = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "mdb_discord_rtp_timestamp_drift_seconds",
		Help: "Drift between wall clock elapsed time and cumulative audio time sent (seconds). Grows during silence gaps when no packets are sent; resets are not expected.",
	})

	promDiscordUsers = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "mdb_discord_users_gauge",
		Help: "The number of Connected Discord users",
	})

	promDiscordReceivedPackets = promauto.NewCounter(prometheus.CounterOpts{
		Name: "mdb_discord_received_count",
		Help: "The number of received packets from Discord",
	})

	promDiscordSentPackets = promauto.NewCounter(prometheus.CounterOpts{
		Name: "mdb_discord_sent_count",
		Help: "The number of packets sent to Discord",
	})

	promDiscordArraySize = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "mdb_discord_array_size_gauge",
		Help: "The discord receiving array size",
	})

	promDiscordStreaming = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "mdb_discord_streaming_gauge",
		Help: "The number of active audio streams streaming from discord",
	})

	promDiscordPLCPackets = promauto.NewCounter(prometheus.CounterOpts{
		Name: "mdb_discord_plc_packets_total",
		Help: "Number of PLC (packet loss concealment) frames generated for lost Discord packets",
	})

	// Sleep Timer Performance

	promTimerDiscordSend = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "mdb_timer_discord_send",
		Help:    "Timer performance for Discord send (10ms target)",
		Buckets: []float64{1000, 2000, 5000, 10000, 20000},
	})

	promTimerDiscordMixer = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "mdb_timer_discord_mixer",
		Help:    "Timer performance for the Discord mixer",
		Buckets: []float64{1000, 2000, 5000, 10000, 20000},
	})

	// Managed Connection Metrics

	promDiscordConnectionStatus = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "mdb_discord_connection_status",
		Help: "Discord connection status (0=disconnected, 1=connecting, 2=connected, 3=reconnecting, 4=failed)",
	})

	promMumbleConnectionStatus = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "mdb_mumble_connection_status",
		Help: "Mumble connection status (0=disconnected, 1=connecting, 2=connected, 3=reconnecting, 4=failed)",
	})

	promDiscordReconnectAttempts = promauto.NewCounter(prometheus.CounterOpts{
		Name: "mdb_discord_reconnect_attempts_total",
		Help: "Total number of Discord reconnection attempts",
	})

	promMumbleReconnectAttempts = promauto.NewCounter(prometheus.CounterOpts{
		Name: "mdb_mumble_reconnect_attempts_total",
		Help: "Total number of Mumble reconnection attempts",
	})

	promDiscordConnectionUptime = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "mdb_discord_connection_uptime_seconds",
		Help: "Duration in seconds that Discord connection has been up",
	})

	promMumbleConnectionUptime = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "mdb_mumble_connection_uptime_seconds",
		Help: "Duration in seconds that Mumble connection has been up",
	})

	promConnectionManagerEvents = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "mdb_connection_manager_events_total",
		Help: "Total number of connection manager events by type and service",
	}, []string{"service", "event_type"})

	promPacketsSunk = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "mdb_packets_sunk_total",
		Help: "Total number of packets sunk due to disconnected state",
	}, []string{"service", "direction"})
)

// StartPromServer starts the Prometheus metrics HTTP server.
func StartPromServer(port int, b *BridgeState) {
	b.Logger.Info("METRICS_SERVER", "Starting Metrics Server")
	http.Handle("/metrics", promhttp.Handler())
	http.HandleFunc("/live", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte("OK")); err != nil {
			b.Logger.Error("METRICS_SERVER", fmt.Sprintf("Error writing response: %v", err))
		}
	})
	http.HandleFunc("/ready", func(w http.ResponseWriter, _ *http.Request) {
		b.BridgeMutex.Lock()
		connected := b.Connected
		b.BridgeMutex.Unlock()

		if connected {
			w.WriteHeader(http.StatusOK)
			if _, err := w.Write([]byte("OK")); err != nil {
				b.Logger.Error("METRICS_SERVER", fmt.Sprintf("Error writing response: %v", err))
			}
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
			if _, err := w.Write([]byte("Disconnected")); err != nil {
				b.Logger.Error("METRICS_SERVER", fmt.Sprintf("Error writing response: %v", err))
			}
		}
	})
	if err := http.ListenAndServe(":"+strconv.Itoa(port), nil); err != nil {
		b.Logger.Error("METRICS_SERVER", fmt.Sprintf("Error starting metrics server: %v", err))
	}
}
