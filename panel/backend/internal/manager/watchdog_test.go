//go:build linux

package manager

import (
	"encoding/base64"
	"github.com/ParsaKSH/spoof-tunnel/internal/l3"
	"github.com/ParsaKSH/spoof-tunnel/panel/internal/db"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWatchdogDecision(t *testing.T) {
	threshold := 120 * time.Second
	cooldown := 5 * time.Minute
	base := time.Now()

	// A tunnel that never connected is never restarted, however long
	// the peer stays lost: there is nothing to recover, only a
	// configuration or path problem a restart cannot fix.
	inst := newInstance(1)
	for _, at := range []time.Duration{0, time.Hour, 2 * time.Hour} {
		if inst.watchdogTick(base.Add(at), false, threshold, cooldown) {
			t.Fatal("watchdog fired before the peer was ever connected")
		}
	}

	// Connected, then lost: no fire before the threshold, fire at it.
	inst = newInstance(1)
	if inst.watchdogTick(base, true, threshold, cooldown) {
		t.Fatal("watchdog fired on a connected peer")
	}
	if inst.watchdogTick(base, false, threshold, cooldown) {
		t.Fatal("watchdog fired the moment the peer was lost")
	}
	if inst.watchdogTick(base.Add(threshold-time.Second), false, threshold, cooldown) {
		t.Fatal("watchdog fired before the threshold")
	}
	if !inst.watchdogTick(base.Add(threshold), false, threshold, cooldown) {
		t.Fatal("watchdog did not fire at the threshold")
	}

	// After firing it must not fire again without a fresh connection.
	if inst.watchdogTick(base.Add(threshold+time.Hour), false, threshold, cooldown) {
		t.Fatal("watchdog fired twice without the peer reconnecting")
	}

	// A reconnect resets the loss clock.
	inst = newInstance(1)
	inst.watchdogTick(base, true, threshold, cooldown)
	inst.watchdogTick(base, false, threshold, cooldown)
	inst.watchdogTick(base.Add(60*time.Second), true, threshold, cooldown)
	inst.watchdogTick(base.Add(61*time.Second), false, threshold, cooldown)
	if inst.watchdogTick(base.Add(61*time.Second+threshold-time.Second), false, threshold, cooldown) {
		t.Fatal("reconnect did not reset the loss clock")
	}
	if !inst.watchdogTick(base.Add(61*time.Second+threshold), false, threshold, cooldown) {
		t.Fatal("watchdog did not fire after the reset threshold")
	}

	// Cooldown: a restart that just happened blocks the next one even
	// when the peer reconnects and drops again immediately.
	inst = newInstance(1)
	inst.watchdogTick(base, true, threshold, cooldown)
	inst.watchdogTick(base, false, threshold, cooldown)
	if !inst.watchdogTick(base.Add(threshold), false, threshold, cooldown) {
		t.Fatal("expected first fire")
	}
	firedAt := base.Add(threshold)
	inst.mu.Lock()
	inst.wdLastRestart = firedAt
	inst.mu.Unlock()
	inst.watchdogTick(firedAt.Add(time.Second), true, threshold, cooldown)
	inst.watchdogTick(firedAt.Add(2*time.Second), false, threshold, cooldown)
	if inst.watchdogTick(firedAt.Add(2*time.Second+threshold), false, threshold, cooldown) {
		t.Fatal("cooldown did not block the second restart")
	}
	if !inst.watchdogTick(firedAt.Add(2*time.Second+threshold+cooldown), false, threshold, cooldown) {
		t.Fatal("watchdog did not fire after the cooldown")
	}
}

func watchdogTestManager(t *testing.T, dir string) (*Manager, db.TunnelInstance) {
	t.Helper()
	database, err := db.InitDB(filepath.Join(dir, "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d, _ := database.DB(); d.Close() })
	script := []byte("#!/bin/sh\ntrap 'exit 0' INT TERM\nwhile :; do sleep 0.05; done\n")
	if err = os.WriteFile(filepath.Join(dir, "spoof-l3"), script, 0700); err != nil {
		t.Fatal(err)
	}
	c := db.TunnelInstance{Name: "watchdog", Engine: "tun", Mode: "local", LocalIP: "192.0.2.1", RemoteAddr: "192.0.2.2", SpoofIP: "198.51.100.1", PeerSpoofIP: "198.51.100.2", SendTransport: "icmp", RecvTransport: "icmp", RemotePort: 8090, RecvPort: 5001, TunInterface: "spwd", TunAddress: "10.77.0.1/30", TunPeer: "10.77.0.2", TunMTU: 1280, PSK: base64.StdEncoding.EncodeToString(make([]byte, 32))}
	if err = database.Create(&c).Error; err != nil {
		t.Fatal(err)
	}
	m := NewManager(database, filepath.Join(dir, "spoof"), dir)
	m.watchdogTick = 20 * time.Millisecond
	m.watchdogThreshold = 200 * time.Millisecond
	m.watchdogCooldown = time.Hour
	t.Cleanup(func() { m.StopAll() })
	return m, c
}

func watchdogSetPeer(t *testing.T, m *Manager, id uint, connected bool) {
	t.Helper()
	inst := m.getInstance(id)
	inst.mu.Lock()
	defer inst.mu.Unlock()
	if connected {
		inst.telemetry = l3.Stats{LastPeer: time.Now().Unix(), PoolReady: true, PoolMode: "adaptive"}
	} else {
		inst.telemetry = l3.Stats{PoolMode: "adaptive"}
	}
}

func TestWatchdogRestartsLostPeerOnce(t *testing.T) {
	dir := t.TempDir()
	m, c := watchdogTestManager(t, dir)
	if err := m.StartInstance(c.ID); err != nil {
		t.Fatal(err)
	}
	inst := m.getInstance(c.ID)
	inst.mu.Lock()
	generation := inst.processGeneration
	inst.mu.Unlock()

	// Connected first: the watchdog must see a live peer.
	watchdogSetPeer(t, m, c.ID, true)
	time.Sleep(100 * time.Millisecond)
	if s := m.Telemetry(c.ID); !s.Peer {
		t.Fatal("test peer was not connected")
	}

	// Then the peer is lost. The watchdog must restart the tunnel once.
	watchdogSetPeer(t, m, c.ID, false)
	deadline := time.Now().Add(5 * time.Second)
	for {
		inst.mu.Lock()
		restarted := inst.processGeneration > generation && inst.wdRestarts == 1
		inst.mu.Unlock()
		if restarted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("watchdog did not restart the lost tunnel")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// While the peer stays lost, the watchdog must not flap: no second
	// restart without a fresh connection.
	time.Sleep(800 * time.Millisecond)
	inst.mu.Lock()
	generationAfter, restarts := inst.processGeneration, inst.wdRestarts
	inst.mu.Unlock()
	if generationAfter != generation+1 || restarts != 1 {
		t.Fatalf("watchdog flapped: generation=%d restarts=%d", generationAfter, restarts)
	}
	if s := m.Telemetry(c.ID); s.WatchdogRestarts != 1 || s.WatchdogLastRestart == 0 {
		t.Fatalf("watchdog restart not reported in telemetry: %+v", s)
	}
	if status, _ := m.InstanceStatus(c.ID); status != StatusRunning {
		t.Fatalf("status after watchdog restart: %s", status)
	}
}

func TestWatchdogNeverFiresBeforeFirstConnection(t *testing.T) {
	dir := t.TempDir()
	m, c := watchdogTestManager(t, dir)
	if err := m.StartInstance(c.ID); err != nil {
		t.Fatal(err)
	}
	inst := m.getInstance(c.ID)
	// The peer never connects: the watchdog must leave the tunnel alone.
	time.Sleep(800 * time.Millisecond)
	inst.mu.Lock()
	generation, restarts := inst.processGeneration, inst.wdRestarts
	inst.mu.Unlock()
	if generation != 1 || restarts != 0 {
		t.Fatalf("watchdog restarted a tunnel that never connected: generation=%d restarts=%d", generation, restarts)
	}
}

func TestWatchdogRestartSkippedAfterManualStop(t *testing.T) {
	dir := t.TempDir()
	m, c := watchdogTestManager(t, dir)
	if err := m.StartInstance(c.ID); err != nil {
		t.Fatal(err)
	}
	inst := m.getInstance(c.ID)
	inst.mu.Lock()
	generation := inst.processGeneration
	inst.mu.Unlock()
	// A manual Stop lands between the watchdog firing and its restart:
	// the restart must not resurrect the tunnel.
	if err := m.StopInstance(c.ID); err != nil {
		t.Fatal(err)
	}
	if err := m.watchdogRestart(c.ID); err != nil {
		t.Fatal(err)
	}
	inst.mu.Lock()
	after, restarts := inst.processGeneration, inst.wdRestarts
	inst.mu.Unlock()
	if after != generation || restarts != 0 {
		t.Fatalf("watchdog restart overrode a manual stop: generation=%d restarts=%d", after, restarts)
	}
	if status, _ := m.InstanceStatus(c.ID); status != StatusStopped {
		t.Fatalf("status after skipped watchdog restart: %s", status)
	}
}

func TestWatchdogPeerTransitionIsLogged(t *testing.T) {
	dir := t.TempDir()
	m, c := watchdogTestManager(t, dir)
	if err := m.StartInstance(c.ID); err != nil {
		t.Fatal(err)
	}
	contains := func(needle string) bool {
		for _, e := range m.Events(100) {
			if e.InstanceID == c.ID && strings.Contains(e.Message, needle) {
				return true
			}
		}
		return false
	}
	waitFor := func(needle string) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for !contains(needle) {
			if time.Now().After(deadline) {
				t.Fatalf("console never reported %q; events: %+v", needle, m.Events(100))
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	// Connected first: the first connection after a start is not a
	// transition and must not be reported as one.
	watchdogSetPeer(t, m, c.ID, true)
	time.Sleep(100 * time.Millisecond)
	if contains("peer connection") {
		t.Fatal("first connection was reported as a transition")
	}
	// Then the peer is lost: the console must say so, with the reason.
	watchdogSetPeer(t, m, c.ID, false)
	waitFor("peer connection lost")
	// And the restore is reported too.
	watchdogSetPeer(t, m, c.ID, true)
	waitFor("peer connection restored")
}
