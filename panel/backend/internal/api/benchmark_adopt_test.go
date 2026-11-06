package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ParsaKSH/spoof-tunnel/internal/benchmark"
	"github.com/ParsaKSH/spoof-tunnel/panel/internal/db"
)

// Regression test for the reported failure: applying a benchmark result
// onto the current connection failed with "TUN subnet overlaps" because
// a create-mode candidate collided with the live pair's own subnet.
// The fix adopts the live pair by identity (real endpoints + TUN subnet)
// instead of treating it as a new pair.
func TestFindBenchmarkPairAdoptsLivePair(t *testing.T) {
	s := benchServer(t)
	local, _ := benchPair(t)
	if e := db.SaveTunnel(s.db, &local, true); e != nil {
		t.Fatal(e)
	}
	// A fresh "create" candidate still collides with the live pair's
	// subnet — the exact failure the user hit on preview.
	fresh, _ := benchPair(t)
	if e := s.validateBenchmarkConfig(fresh, true); e == nil || !strings.Contains(e.Error(), "TUN subnet overlaps") {
		t.Fatalf("fresh create did not fail on overlap: %v", e)
	}
	// ...but the same pair is adopted by identity.
	found, ok, e := s.findBenchmarkPair("local", local.LocalIP, local.RemoteAddr, local.TunAddress, "")
	if e != nil || !ok {
		t.Fatalf("live pair not adopted: found=%v err=%v", ok, e)
	}
	if found.ID != local.ID {
		t.Fatalf("adopted wrong tunnel: %d != %d", found.ID, local.ID)
	}
	// Replacing the adopted pair validates (self-excluded).
	adopted := found
	adopted.SendTransport = "tcp"
	adopted.RecvTransport = "udp"
	change, e := s.makeBenchChange(adopted, found.ID)
	if e != nil {
		t.Fatal(e)
	}
	if !change.existed {
		t.Fatal("adopted change was not a replacement")
	}
	// A different subnet is genuinely new.
	_, ok, e = s.findBenchmarkPair("local", local.LocalIP, local.RemoteAddr, "10.99.0.0/30", "")
	if e != nil || ok {
		t.Fatalf("different subnet wrongly adopted: ok=%v err=%v", ok, e)
	}
	// Two identical pairs are ambiguous and must not be guessed.
	second := local
	second.ID = 0
	second.Name = "bench · copy"
	second.TunInterface = "spcopy01"
	if e := s.db.Create(&second).Error; e != nil {
		t.Fatal(e)
	}
	if _, _, e := s.findBenchmarkPair("local", local.LocalIP, local.RemoteAddr, local.TunAddress, ""); e == nil || !strings.Contains(e.Error(), "benchmark_multiple_matching_connections") {
		t.Fatalf("ambiguous pair did not refuse: %v", e)
	}
}

func TestFindBenchmarkPairRemoteRequiresPSK(t *testing.T) {
	s := benchServer(t)
	_, remote := benchPair(t)
	if e := db.SaveTunnel(s.db, &remote, true); e != nil {
		t.Fatal(e)
	}
	found, ok, e := s.findBenchmarkPair("remote", remote.LocalIP, remote.ClientIP, remote.TunAddress, remote.PSK)
	if e != nil || !ok || found.ID != remote.ID {
		t.Fatalf("remote pair not adopted with matching PSK: ok=%v err=%v", ok, e)
	}
	_, ok, e = s.findBenchmarkPair("remote", remote.LocalIP, remote.ClientIP, remote.TunAddress, strings.Repeat("A", 44))
	if e != nil || ok {
		t.Fatalf("remote pair adopted with wrong PSK: ok=%v err=%v", ok, e)
	}
}

// The receiving side must adopt its live pair when the code was generated
// without an explicit target — otherwise the prepare fails on the live
// tunnel's own subnet, exactly like the controller side did.
func TestBenchmarkReceiverAdoptsLivePairWithoutTarget(t *testing.T) {
	s := benchServer(t)
	local, remote := benchPair(t)
	if e := db.SaveTunnel(s.db, &remote, true); e != nil {
		t.Fatal(e)
	}
	inv := benchmark.Invitation{A: local.LocalIP, B: remote.LocalIP, SourceA: local.SpoofIP, SourceB: remote.SpoofIP}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hook := s.receiverBenchmarkHook(ctx, 0, time.Time{}, inv, true)
	plan := benchmarkPlan{Token: "adopted", Index: 1, Local: local, Remote: remote}
	body, _ := json.Marshal(plan.offer())
	if _, e := hook(ctx, "apply_prepare", body); e != nil {
		t.Fatal(e)
	}
}
