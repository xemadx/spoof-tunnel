package manager

import (
	"strings"
	"testing"
)

func TestGlobalLogEventsRing(t *testing.T) {
	m := NewManager(nil, "unused", t.TempDir())
	m.RecordEvent(7, LogError, "boom")
	m.RecordEvent(7, LogInfo, "ok")
	events := m.Events(10)
	if len(events) != 2 || events[0].Level != LogError || events[0].InstanceID != 7 || events[1].Message != "ok" {
		t.Fatalf("unexpected events: %+v", events)
	}
	if events[0].Time.IsZero() {
		t.Fatal("event has no timestamp")
	}
	// The ring is bounded: old entries drop off.
	for i := 0; i < maxGlobalLogEvents+50; i++ {
		m.RecordEvent(0, LogInfo, "filler")
	}
	if got := len(m.Events(0)); got != maxGlobalLogEvents {
		t.Fatalf("ring not capped: %d events", got)
	}
	// Empty messages are not recorded.
	m.RecordEvent(0, LogError, "   ")
	if got := len(m.Events(0)); got != maxGlobalLogEvents {
		t.Fatalf("blank event changed the ring: %d", got)
	}
}

func TestStreamLogsFeedsGlobalConsole(t *testing.T) {
	m := NewManager(nil, "unused", t.TempDir())
	inst := m.getInstance(3)
	streamLogs(inst, strings.NewReader("[watchdog] peer connection lost (pool ready: true, last rejected source: none)\n{\"event\":\"spoof_stats\",\"peer_connected\":true}\nplain line\n"))
	events := m.Events(10)
	// The telemetry JSON line is not a human log and is skipped.
	if len(events) != 2 {
		t.Fatalf("expected 2 console entries, got %d: %+v", len(events), events)
	}
	if events[0].Level != LogWarn || !strings.Contains(events[0].Message, "peer connection lost") {
		t.Fatalf("lost line not classified as a warning: %+v", events[0])
	}
	if events[0].InstanceID != 3 {
		t.Fatalf("wrong instance on console entry: %d", events[0].InstanceID)
	}
	if events[1].Level != LogInfo || events[1].Message != "plain line" {
		t.Fatalf("plain line wrong: %+v", events[1])
	}
}

func TestClassifyLogLevel(t *testing.T) {
	cases := []struct {
		line string
		want LogLevel
	}{
		{"[supervisor] automatic recovery failed: exit status 1", LogError},
		{"[watchdog] peer connection lost", LogWarn},
		{"source rejected: 198.51.100.9", LogWarn},
		{"tunnel started", LogInfo},
	}
	for _, tc := range cases {
		if got := classifyLogLevel(tc.line); got != tc.want {
			t.Fatalf("classify %q: got %s, want %s", tc.line, got, tc.want)
		}
	}
}
