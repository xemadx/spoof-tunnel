//go:build linux

package manager

import (
	"encoding/base64"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ParsaKSH/spoof-tunnel/panel/internal/db"
)

func TestForwarderLifecycleAndConcurrentStop(t *testing.T) {
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

	c := db.TunnelInstance{
		Name:          "fwd-test",
		Engine:        "tun",
		Mode:          "local",
		LocalIP:       "192.0.2.1",
		RemoteAddr:    "192.0.2.2",
		SpoofIP:       "198.51.100.1",
		PeerSpoofIP:   "198.51.100.2",
		SendTransport: "icmp",
		RecvTransport: "icmp",
		RemotePort:    8090,
		RecvPort:      5001,
		TunInterface:  "sptest",
		TunAddress:    "10.77.0.1/30",
		TunPeer:       "10.77.0.2",
		TunMTU:        1280,
		PSK:           base64.StdEncoding.EncodeToString(make([]byte, 32)),
	}
	if err = database.Create(&c).Error; err != nil {
		t.Fatal(err)
	}

	m := NewManager(database, filepath.Join(dir, "spoof"), dir)
	defer m.StopAll()

	// Forwarder cannot start if tunnel is not running
	rule := db.ForwardRule{
		InstanceID: c.ID,
		Name:       "web",
		Protocol:   "tcp",
		ListenPort: 0,
		TargetPort: 8080,
		Enabled:    true,
	}
	if err = m.StartForwarder(c, rule); err == nil {
		t.Fatal("expected error starting forwarder when tunnel is stopped")
	}

	// Start instance
	if err = m.StartInstance(c.ID); err != nil {
		t.Fatal(err)
	}

	// Pick a free local port
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	rule.ListenPort = port
	if err = database.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}

	// Start forwarder
	if err = m.StartForwarder(c, rule); err != nil {
		t.Fatalf("StartForwarder: %v", err)
	}

	// Dial forwarder in background while simultaneously stopping it
	// to verify that IsReady and fwd.Stop() never deadlock.
	stopDone := make(chan struct{})
	go func() {
		defer close(stopDone)
		// Give the dial a moment to trigger pipe()
		time.Sleep(10 * time.Millisecond)
		m.StopForwarders(c.ID)
	}()

	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 500*time.Millisecond)
	if err == nil {
		defer conn.Close()
	}

	select {
	case <-stopDone:
		// Succeeded promptly without deadlock
	case <-time.After(3 * time.Second):
		t.Fatal("deadlock in StopForwarders during concurrent connection")
	}

	// Restart forwarders via StartForwarders
	m.StartForwarders(c.ID)

	// StopInstance must cleanly stop forwarders without deadlock
	stopInstDone := make(chan struct{})
	go func() {
		defer close(stopInstDone)
		_ = m.StopInstance(c.ID)
	}()

	select {
	case <-stopInstDone:
		// Clean stop
	case <-time.After(3 * time.Second):
		t.Fatal("deadlock in StopInstance with active forwarders")
	}
}
