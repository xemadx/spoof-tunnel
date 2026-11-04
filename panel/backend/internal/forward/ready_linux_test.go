//go:build linux

package forward

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestDelayedTunnelBecomesReady(t *testing.T) {
	if os.Getenv("SPOOF_FORWARD_ISOLATED") != "1" {
		t.Skip("requires private network namespace and CAP_NET_ADMIN")
	}
	name := "sp-ready-test"
	ip := func(args ...string) {
		t.Helper()
		if output, err := exec.Command("ip", args...).CombinedOutput(); err != nil {
			t.Fatalf("ip %v: %s %v", args, output, err)
		}
	}
	ip("tuntap", "add", "dev", name, "mode", "tun")
	defer exec.Command("ip", "link", "del", name).Run()
	done := make(chan error, 1)
	go func() { done <- waitTunnelReady(context.Background(), name, 3*time.Second) }()
	select {
	case err := <-done:
		t.Fatalf("accepted a down TUN: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	ip("link", "set", name, "up")
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("did not detect delayed TUN startup")
	}
}
