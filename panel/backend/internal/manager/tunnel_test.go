//go:build linux

package manager

import (
	"encoding/base64"
	"github.com/ParsaKSH/spoof-tunnel/panel/internal/db"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestRestartWaitsAndConfigIsPrivate(t *testing.T) {
	dir := t.TempDir()
	database, err := db.InitDB(filepath.Join(dir, "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { d, _ := database.DB(); d.Close() }()
	script := []byte("#!/bin/sh\ntrap 'exit 0' INT TERM\nwhile :; do sleep 0.05; done\n")
	if err = os.WriteFile(filepath.Join(dir, "spoof-l3"), script, 0700); err != nil {
		t.Fatal(err)
	}
	c := db.TunnelInstance{Name: "test", Engine: "tun", Mode: "local", LocalIP: "192.0.2.1", RemoteAddr: "192.0.2.2", SpoofIP: "198.51.100.1", PeerSpoofIP: "198.51.100.2", SendTransport: "icmp", RecvTransport: "icmp", RemotePort: 8090, RecvPort: 5001, TunInterface: "sptest", TunAddress: "10.77.0.1/30", TunPeer: "10.77.0.2", TunMTU: 1280, PSK: base64.StdEncoding.EncodeToString(make([]byte, 32))}
	if err = database.Create(&c).Error; err != nil {
		t.Fatal(err)
	}
	m := NewManager(database, filepath.Join(dir, "spoof"), dir)
	defer m.StopAll()
	if err = m.StartInstance(c.ID); err != nil {
		t.Fatal(err)
	}
	if err = m.StartInstance(c.ID); err == nil {
		t.Fatal("duplicate start accepted")
	}
	time.Sleep(100 * time.Millisecond)
	if err = m.RestartInstance(c.ID); err != nil {
		t.Fatal(err)
	}
	if status, _ := m.InstanceStatus(c.ID); status != StatusRunning {
		t.Fatalf("new process status: %s", status)
	}
	if err = m.StopInstance(c.ID); err != nil {
		t.Fatal(err)
	}
	if status, _ := m.InstanceStatus(c.ID); status != StatusStopped {
		t.Fatalf("stop status: %s", status)
	}
	info, err := os.Stat(m.configFilePath(c.ID))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("config permissions: %v %v", info, err)
	}
}

func TestExitedProcessCannotLeaveForwardersListening(t *testing.T) {
	previous := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(previous)
	dir := t.TempDir()
	database, err := db.InitDB(filepath.Join(dir, "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { d, _ := database.DB(); d.Close() }()
	if err = os.WriteFile(filepath.Join(dir, "spoof-l3"), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	config := db.TunnelInstance{Name: "short-lived", Engine: "tun", Mode: "local", LocalIP: "192.0.2.1", RemoteAddr: "192.0.2.2", SpoofIP: "198.51.100.1", PeerSpoofIP: "198.51.100.2", SendTransport: "icmp", RecvTransport: "icmp", RemotePort: 8090, RecvPort: 5001, TunInterface: "sptest", TunAddress: "10.77.0.1/30", TunPeer: "10.77.0.2", TunMTU: 1280, PSK: base64.StdEncoding.EncodeToString(make([]byte, 32))}
	database.Create(&config)
	database.Create(&db.ForwardRule{InstanceID: config.ID, Name: "entry", Protocol: "tcp", ListenPort: 0, TargetPort: 443, Enabled: true})
	manager := NewManager(database, "unused", dir)
	defer manager.StopAll()
	for attempt := 0; attempt < 20; attempt++ {
		if err = manager.StartInstance(config.ID); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(2 * time.Second)
		for {
			status, _ := manager.InstanceStatus(config.ID)
			if status == StatusStopped {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("process was not reaped")
			}
			time.Sleep(time.Millisecond)
		}
		for _, status := range manager.ForwardStatuses(config.ID) {
			if status.Listening {
				t.Fatal("forwarder remained open after process exit")
			}
		}
	}
}

func TestStartupFailureIsVisibleInStatus(t *testing.T) {
	dir := t.TempDir()
	database, err := db.InitDB(filepath.Join(dir, "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { d, _ := database.DB(); d.Close() }()
	invalid := db.TunnelInstance{Name: "invalid", Engine: "tun", Mode: "local"}
	database.Create(&invalid)
	manager := NewManager(database, "unused", dir)
	if manager.StartInstance(invalid.ID) == nil {
		t.Fatal("invalid configuration started")
	}
	status, message := manager.InstanceStatus(invalid.ID)
	if status != StatusError || message == "" {
		t.Fatalf("failure was hidden: %s %q", status, message)
	}
}

func TestCrashRecoveryAndManualStopCancelsRetry(t *testing.T) {
	dir := t.TempDir()
	database, err := db.InitDB(filepath.Join(dir, "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { d, _ := database.DB(); d.Close() }()
	script := "#!/bin/sh\nif [ ! -f '" + filepath.Join(dir, "once") + "' ]; then touch '" + filepath.Join(dir, "once") + "'; exit 1; fi\ntrap 'exit 0' INT TERM\nwhile :; do sleep 1; done\n"
	if err := os.WriteFile(filepath.Join(dir, "spoof-l3"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	cfg := db.TunnelInstance{Name: "recover", Engine: "tun", Mode: "local", LocalIP: "192.0.2.1", RemoteAddr: "192.0.2.2", SpoofIP: "198.51.100.1", PeerSpoofIP: "198.51.100.2", SendTransport: "icmp", RecvTransport: "icmp", RemotePort: 8090, RecvPort: 5001, TunInterface: "sprec", TunAddress: "10.77.0.1/30", TunPeer: "10.77.0.2", TunMTU: 1280, PSK: base64.StdEncoding.EncodeToString(make([]byte, 32))}
	database.Create(&cfg)
	manager := NewManager(database, "unused", dir)
	defer manager.StopAll()
	if err := manager.StartInstance(cfg.ID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		inst := manager.getInstance(cfg.ID)
		inst.mu.Lock()
		ok := inst.retryCount > 0 && inst.cmd != nil
		inst.mu.Unlock()
		if ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("did not recover")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := manager.StopInstance(cfg.ID); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1500 * time.Millisecond)
	if status, _ := manager.InstanceStatus(cfg.ID); status != StatusStopped {
		t.Fatalf("restarted after manual stop: %s", status)
	}

	guard := manager.MutationGuard()
	guard.Lock()
	if err := os.Remove(filepath.Join(dir, "once")); err != nil {
		t.Fatal(err)
	}
	if err := manager.StartInstance(cfg.ID); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(time.Second)
	for {
		status, _ := manager.InstanceStatus(cfg.ID)
		if status == StatusError {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("test process did not crash")
		}
		time.Sleep(time.Millisecond)
	}
	time.Sleep(1200 * time.Millisecond)
	if status, _ := manager.InstanceStatus(cfg.ID); status != StatusError {
		guard.Unlock()
		t.Fatal("recovery bypassed API mutation guard")
	}
	if err := manager.StopInstance(cfg.ID); err != nil {
		guard.Unlock()
		t.Fatal(err)
	}
	guard.Unlock()
	time.Sleep(1500 * time.Millisecond)
	if status, _ := manager.InstanceStatus(cfg.ID); status != StatusStopped {
		t.Fatal("pending retry ignored manual Stop")
	}
}

func TestTelemetryAcceptsReorderedJSON(t *testing.T) {
	inst := newInstance(1)
	streamLogs(inst, strings.NewReader("prefix {\"source_rejected\":9,\"event\":\"spoof_stats\",\"clock_skew_seconds\":60,\"last_peer\":100}\n"))
	if inst.telemetry.SourceRejected != 9 || inst.telemetry.ClockSkewSeconds != 60 {
		t.Fatalf("lost diagnostics: %+v", inst.telemetry)
	}
}

func TestOldProcessTelemetryCannotOverwriteRecoveredGeneration(t *testing.T) {
	inst := newInstance(1)
	inst.processGeneration = 2
	streamLogs(inst, strings.NewReader("{\"event\":\"spoof_stats\",\"tx_bytes\":9}\n"), 2)
	streamLogs(inst, strings.NewReader("{\"event\":\"spoof_stats\",\"tx_bytes\":100,\"last_peer\":9999999999}\n"), 1)
	if inst.telemetry.TX != 9 || inst.telemetry.LastPeer != 0 {
		t.Fatalf("old process overwrote new telemetry: %+v", inst.telemetry)
	}
}
