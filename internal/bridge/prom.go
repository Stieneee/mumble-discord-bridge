package bridge

import (
	"log"
	"net/http"
	"strconv"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	// Bridge General

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

	// promToMumbleBufferSize = promauto.NewGauge(prometheus.GaugeOpts{
	// 	Name: "mdb_to_mumble_buffer_gauge",
	// 	Help: "",
	// })

	promToMumbleDropped = promauto.NewCounter(prometheus.CounterOpts{
		Name: "mdb_to_mumble_dropped",
		Help: "The number of packets timeouts to mumble",
	})

	promMumbleArraySize = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "mdb_to_mumble_array_size_gauge",
		Help: "The array size of mumble streams",
	})

	promMumbleStreaming = promauto.NewGauge(prometheus.GaugeOpts{ //SUMMARY?
		Name: "mdb_mumble_streaming_gauge",
		Help: "The number of active audio streams streaming audio from mumble",
	})

	// DISCORD

	// TODO Discrod Ping

	promDiscordHeartBeat = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "mdb_discord_latency",
		Help: "Discord heartbeat latency",
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

	promToDiscordBufferSize = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "mdb_discord_buffer_gauge",
		Help: "The buffer size for packets to Discord",
	})

	promToDiscordDropped = promauto.NewCounter(prometheus.CounterOpts{
		Name: "mdb_to_discord_dropped",
		Help: "The count of packets dropped to discord",
	})

	promDiscordArraySize = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "mdb_discord_array_size_gauge",
		Help: "The discord receiving array size",
	})

	promDiscordStreaming = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "mdb_discord_streaming_gauge",
		Help: "The number of active audio streams streaming from discord",
	})

	// Sleep Timer Performance

	promTimerDiscordSend = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "mdb_timer_discord_send",
		Help:    "Timer performance for Discord send",
		Buckets: []float64{1000, 2000, 5000, 10000, 20000},
	})

	promTimerDiscordMixer = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "mdb_timer_discord_mixer",
		Help:    "Timer performance for the Discord mixer",
		Buckets: []float64{1000, 2000, 5000, 10000, 20000},
	})

	promTimerMumbleMixer = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "mdb_timer_mumble_mixer",
		Help:    "Timer performance for the Mumble mixer",
		Buckets: []float64{1000, 2000, 5000, 10000, 20000},
	})
)

func StartPromServer(port int, b *BridgeState) {
	log.Println("Starting Metrics Server")
	http.Handle("/metrics", promhttp.Handler())
	http.HandleFunc("/live", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})
	http.HandleFunc("/ready", func(w http.ResponseWriter, r *http.Request) {
		if b.Connected {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("OK"))
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte("Disconnected"))
		}
	})
	http.ListenAndServe(":"+strconv.Itoa(port), nil)
}
