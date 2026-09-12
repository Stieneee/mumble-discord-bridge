package bridgelib

import "testing"

// newTestDispatcher builds a started EventDispatcher, mirroring the construction
// pattern used throughout events_test.go (NewEventDispatcher + Start), and
// registers a cleanup to stop it.
func newTestDispatcher(t *testing.T) *EventDispatcher {
	t.Helper()
	ed := NewEventDispatcher("test-bridge", 10, nil)
	ed.Start()
	t.Cleanup(ed.Stop)
	return ed
}

func TestEventPresenceAnnouncedString(t *testing.T) {
	if got := EventPresenceAnnounced.String(); got != "PresenceAnnounced" {
		t.Fatalf("String() = %q, want %q", got, "PresenceAnnounced")
	}
}

func TestEmitPresenceEventDispatchesRosters(t *testing.T) {
	inst := &BridgeInstance{eventDispatcher: newTestDispatcher(t)}
	got := make(chan BridgeEvent, 1)
	inst.RegisterHandler(EventPresenceAnnounced, func(e BridgeEvent) { got <- e })

	inst.EmitPresenceEvent([]string{"alice", "probe-m2d"}, []string{"Mumble-Bridge"})

	e := <-got
	if e.Type != EventPresenceAnnounced {
		t.Fatalf("type = %v", e.Type)
	}
	m, _ := e.Data["mumble_users"].([]string)
	d, _ := e.Data["discord_users"].([]string)
	if len(m) != 2 || m[0] != "alice" || len(d) != 1 || d[0] != "Mumble-Bridge" {
		t.Fatalf("rosters = %v / %v", m, d)
	}
}
