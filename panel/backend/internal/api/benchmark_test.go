package api

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ParsaKSH/spoof-tunnel/internal/benchmark"
	"github.com/ParsaKSH/spoof-tunnel/panel/internal/db"
	"github.com/ParsaKSH/spoof-tunnel/panel/internal/manager"
	"gorm.io/gorm"
)

func benchServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	database, e := db.InitDB(filepath.Join(dir, "panel.db"))
	if e != nil {
		t.Fatal(e)
	}
	s := NewServer(database, manager.NewManager(database, "unused", dir), "/test", "test")
	t.Cleanup(func() { s.Close(); sql, _ := database.DB(); sql.Close() })
	return s
}
func benchPair(t *testing.T) (db.TunnelInstance, db.TunnelInstance) {
	t.Helper()
	a, b, e := buildPair(pairRequest{Name: "bench", Engine: "tun", LocalIP: "127.0.0.91", RemoteIP: "127.0.0.92", LocalSpoof: "127.1.91.1", RemoteSpoof: "127.1.92.1", Upload: "icmp", Download: "udp", LocalPort: 39991, RemotePort: 39992})
	if e != nil {
		t.Fatal(e)
	}
	return a, b
}
func TestBenchmarkPreviewAndFailedStartPreserveConfiguration(t *testing.T) {
	s := benchServer(t)
	old, _ := benchPair(t)
	if e := db.SaveTunnel(s.db, &old, true); e != nil {
		t.Fatal(e)
	}
	candidate := old
	candidate.SendTransport = "tcp"
	change, e := s.makeBenchChange(candidate, old.ID)
	if e != nil {
		t.Fatal(e)
	}
	change.token = "failed-start"
	var current db.TunnelInstance
	s.db.First(&current, old.ID)
	if current.SendTransport != "icmp" {
		t.Fatal("preview changed live config")
	}
	if e = change.commit(); e == nil {
		t.Fatal("nonexistent core binary unexpectedly started")
	}
	s.db.First(&current, old.ID)
	if current.SendTransport != "icmp" || current.PSK != old.PSK {
		t.Fatal("failed start did not restore original connection")
	}
	var journals int64
	s.db.Model(&db.Setting{}).Where("key LIKE ?", "benchmark-apply-%").Count(&journals)
	if journals != 0 {
		t.Fatal("rollback left pending journal")
	}
	newCandidate, _ := benchPair(t)
	newCandidate.TunInterface = "spnewtest"
	newCandidate.TunAddress = "10.77.0.5/30"
	newCandidate.TunPeer = "10.77.0.6"
	next, e := s.makeBenchChange(newCandidate, 0)
	if e != nil {
		t.Fatal(e)
	}
	next.token = "failed-new"
	if next.commit() == nil {
		t.Fatal("missing core accepted")
	}
	var count int64
	s.db.Model(&db.TunnelInstance{}).Count(&count)
	if count != 1 {
		t.Fatal("failed create left an orphan connection")
	}
}
func TestBenchmarkRecoveryAndNewerEditProtection(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "new", true: "existing"}[existing], func(t *testing.T) {
			s := benchServer(t)
			before, _ := benchPair(t)
			if existing {
				if e := db.SaveTunnel(s.db, &before, true); e != nil {
					t.Fatal(e)
				}
			}
			after := before
			after.SendTransport = "tcp"
			change := &benchChange{s: s, before: before, after: after, existed: existing, token: "interrupted"}
			if e := s.db.Transaction(func(tx *gorm.DB) error {
				if e := db.SaveTunnel(tx, &change.after, !existing); e != nil {
					return e
				}
				return change.journal(tx, true)
			}); e != nil {
				t.Fatal(e)
			}
			s.recoverBenchmarkChanges()
			var rows []db.TunnelInstance
			s.db.Find(&rows)
			if existing && (len(rows) != 1 || rows[0].SendTransport != "icmp") {
				t.Fatal("existing config not recovered")
			}
			if !existing && len(rows) != 0 {
				t.Fatal("interrupted new config not removed")
			}
		})
	}
	s := benchServer(t)
	candidate, _ := benchPair(t)
	if e := db.SaveTunnel(s.db, &candidate, true); e != nil {
		t.Fatal(e)
	}
	change := &benchChange{s: s, after: candidate, committed: true, token: "new-edit", existed: false}
	newer := candidate
	newer.Name = "newer user edit"
	if e := db.SaveTunnel(s.db, &newer, false); e != nil {
		t.Fatal(e)
	}
	if e := change.rollback(); e == nil {
		t.Fatal("rollback deleted a newer edit")
	}
	var saved db.TunnelInstance
	if s.db.First(&saved, candidate.ID).Error != nil || saved.Name != newer.Name {
		t.Fatal("newer edit was lost")
	}
}
func TestBenchmarkReceiverPlanScopeAndComplement(t *testing.T) {
	s := benchServer(t)
	local, remote := benchPair(t)
	if e := db.SaveTunnel(s.db, &remote, true); e != nil {
		t.Fatal(e)
	}
	inv := benchmark.Invitation{A: local.LocalIP, B: remote.LocalIP, SourceA: local.SpoofIP, SourceB: remote.SpoofIP}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hook := s.receiverBenchmarkHook(ctx, remote.ID, remote.UpdatedAt, inv, true)
	plan := benchmarkPlan{Token: "scoped", Index: 1, Local: local, Remote: remote}
	body, _ := json.Marshal(plan.offer())
	if _, e := hook(ctx, "apply_prepare", body); e != nil {
		t.Fatal(e)
	}
	plan.Token = "wrong-pair"
	plan.Remote.PSK = strings.Repeat("A", 44)
	body, _ = json.Marshal(plan.offer())
	if _, e := hook(ctx, "apply_prepare", body); e == nil {
		t.Fatal("mismatched pair key accepted")
	}
	denied := s.receiverBenchmarkHook(ctx, 0, time.Time{}, inv, false)
	if _, e := denied(ctx, "apply_prepare", body); e == nil {
		t.Fatal("apply capability was not required")
	}
}

func TestBenchmarkOfferNeverCarriesSourceInventories(t *testing.T) {
	a, b := benchPair(t)
	a.SpoofIPList = strings.Repeat("198.51.100.1\n", 200000)
	b.SpoofIPList = a.SpoofIPList
	b.PeerSpoofIPList = a.SpoofIPList
	b.Name = strings.Repeat("آ", 96)
	p := benchmarkPlan{Token: strings.Repeat("a", 32), Index: 24, Local: a, Remote: b}
	data, e := json.Marshal(p.offer())
	if e != nil || len(data) > 800 || strings.Contains(string(data), "spoof_ip_list") {
		t.Fatalf("unbounded raw control proposal: bytes=%d error=%v", len(data), e)
	}
}

func TestBoundedPoolSelectionPersistsAtomically(t *testing.T) {
	s := benchServer(t)
	local, _ := benchPair(t)
	if e := db.SaveTunnel(s.db, &local, true); e != nil {
		t.Fatal(e)
	}
	var group db.PoolFile
	if e := s.db.Where("instance_id = ? AND kind = ?", local.ID, "default").First(&group).Error; e != nil {
		t.Fatal(e)
	}
	req := poolSelection{IDs: []uint{group.ID}, Mode: "round_robin", PPS: 25, ActiveSize: 100, RefreshSeconds: 1800, AutoSync: true}
	if e := s.db.Transaction(func(tx *gorm.DB) error { return savePoolSelection(tx, &local, req) }); e != nil {
		t.Fatal(e)
	}
	var saved db.TunnelInstance
	s.db.First(&saved, local.ID)
	policy := saved.L3Config().PoolPolicy.Normalized()
	if policy.ActiveSize != 100 || policy.RefreshSeconds != 1800 || policy.Mode != "round_robin" {
		t.Fatal("cohort policy lost between API storage and engine config", policy)
	}
	req.ActiveSize = 101
	if e := s.db.Transaction(func(tx *gorm.DB) error { return savePoolSelection(tx, &local, req) }); e == nil {
		t.Fatal("over-budget selection accepted")
	}
	s.db.First(&saved, local.ID)
	if saved.PoolActiveSize != 100 {
		t.Fatal("invalid selection partially changed configuration")
	}
	s.db.First(&group, group.ID)
	if !group.Enabled || group.Count != 1 {
		t.Fatal("invalid selection changed default group")
	}
}

func installBenchFixture(t *testing.T, s *Server) {
	t.Helper()
	script := `#!/usr/bin/python3
import json, signal, time
signal.signal(signal.SIGINT,lambda *_:exit(0))
while True:
 print(json.dumps(dict(event="spoof_stats",peer_connected=True,last_peer=int(time.time()))),flush=True)
 time.sleep(.1)
`
	if e := os.WriteFile(filepath.Join(s.manager.DataDir(), "spoof-l3"), []byte(script), 0700); e != nil {
		t.Fatal(e)
	}
}

func TestBenchmarkStaleLocalPreviewCannotOverwriteLaterEdit(t *testing.T) {
	s := benchServer(t)
	old, _ := benchPair(t)
	if e := db.SaveTunnel(s.db, &old, true); e != nil {
		t.Fatal(e)
	}
	proposal := old
	proposal.SendTransport = "tcp"
	newer := old
	newer.Name = "later edit"
	if e := db.SaveTunnel(s.db, &newer, false); e != nil {
		t.Fatal(e)
	}
	if _, e := s.makeBenchChange(proposal, old.ID); e == nil {
		t.Fatal("stale local preview overwrote a newer edit")
	}
}

func TestBenchmarkRecoveryResumesManuallyStartedConnection(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(map[bool]string{false: "intent", true: "candidate"}[committed], func(t *testing.T) {
			s := benchServer(t)
			installBenchFixture(t, s)
			before, _ := benchPair(t)
			before.Enabled = false
			if e := db.SaveTunnel(s.db, &before, true); e != nil {
				t.Fatal(e)
			}
			change := &benchChange{s: s, before: before, after: before, existed: true, running: true, token: "resume"}
			if committed {
				change.after.SendTransport = "tcp"
				if e := db.SaveTunnel(s.db, &change.after, false); e != nil {
					t.Fatal(e)
				}
			}
			if e := change.journal(s.db, committed); e != nil {
				t.Fatal(e)
			}
			s.recoverBenchmarkChanges()
			status, _ := s.manager.InstanceStatus(before.ID)
			if status != "running" {
				t.Fatal("previously running manual connection was left stopped")
			}
			var saved db.TunnelInstance
			s.db.First(&saved, before.ID)
			if saved.Enabled || saved.SendTransport != "icmp" {
				t.Fatal("recovery changed the original autostart policy")
			}
		})
	}
}
