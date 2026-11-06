package api

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ParsaKSH/spoof-tunnel/internal/benchmark"
	"github.com/ParsaKSH/spoof-tunnel/internal/l3"
	"github.com/ParsaKSH/spoof-tunnel/panel/internal/auth"
	"github.com/ParsaKSH/spoof-tunnel/panel/internal/db"
)

func benchmarkRequest(t *testing.T, s *Server, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var user db.User
	s.db.First(&user)
	if user.ID == 0 {
		user = db.User{Username: "preset-test", PasswordHash: "unused"}
		if e := s.db.Create(&user).Error; e != nil {
			t.Fatal(e)
		}
	}
	token, _ := auth.GenerateToken(user.ID, user.Username)
	data, _ := json.Marshal(body)
	req := httptest.NewRequest(method, "/test/api"+path, bytes.NewReader(data))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	out := httptest.NewRecorder()
	s.Router().ServeHTTP(out, req)
	return out
}
func TestBenchmarkPresetUsesSelectedPoolInsteadOfRemovedFallback(t *testing.T) {
	s := benchServer(t)
	_, remote := benchPair(t)
	remote.SpoofIPList = "127.1.91.7\n127.1.91.8"
	remote.PeerSpoofIPList = "127.1.92.9"
	if e := db.SaveTunnel(s.db, &remote, true); e != nil {
		t.Fatal(e)
	}
	out := benchmarkRequest(t, s, "GET", "/instances/1/benchmark-presets", nil)
	if out.Code != 200 {
		t.Fatal(out.Code, out.Body.String())
	}
	var p benchmarkPreset
	if e := json.Unmarshal(out.Body.Bytes(), &p); e != nil {
		t.Fatal(e)
	}
	if p.Source != "127.1.91.7" || p.PeerSource != "127.1.92.9" || p.LocalIP != remote.LocalIP || p.PeerIP != remote.ClientIP || p.SendPort != remote.ClientPort || p.ReceivePort != remote.ListenPort || p.Role != "remote" {
		t.Fatalf("stale or inverted preset: %+v", p)
	}
	if strings.Contains(out.Body.String(), remote.PSK) || strings.Contains(out.Body.String(), "spoof_ip_list") || strings.Contains(out.Body.String(), "127.1.91.8") {
		t.Fatal("preset disclosed credentials or a source inventory")
	}
	if e := benchmarkEndpoint(remote, p.LocalIP, p.PeerIP, p.Source, p.PeerSource); e != nil {
		t.Fatal("panel generated a preset rejected by its validator", e)
	}
	for _, trial := range []struct{ local, peer, source, peerSource, reason string }{
		{"127.0.0.5", p.PeerIP, p.Source, p.PeerSource, "benchmark_local_ip_mismatch"},
		{p.LocalIP, "127.0.0.5", p.Source, p.PeerSource, "benchmark_peer_ip_mismatch"},
		{p.LocalIP, p.PeerIP, remote.SpoofIP, p.PeerSource, "benchmark_source_not_in_pool"},
		{p.LocalIP, p.PeerIP, p.Source, remote.PeerSpoofIP, "benchmark_peer_source_not_admitted"},
	} {
		if e := benchmarkEndpoint(remote, trial.local, trial.peer, trial.source, trial.peerSource); e == nil || e.Error() != trial.reason {
			t.Fatalf("incorrect mismatch diagnosis: %v", e)
		}
	}
	var saved db.TunnelInstance
	s.db.First(&saved, remote.ID)
	if saved.SpoofIP != remote.SpoofIP || saved.SpoofIPList != remote.SpoofIPList || !saved.UpdatedAt.Equal(remote.UpdatedAt) {
		t.Fatal("reading preset changed stored connection")
	}
}

type presetWire struct {
	done chan struct{}
	once sync.Once
}

func (w *presetWire) Send([]byte) error        { return nil }
func (w *presetWire) Receive() ([]byte, error) { <-w.done; return nil, net.ErrClosed }
func (w *presetWire) Close()                   { w.once.Do(func() { close(w.done) }) }
func TestBenchmarkPresetAndReceiverUseEffectivePeerManifest(t *testing.T) {
	s := benchServer(t)
	installBenchFixture(t, s)
	_, remote := benchPair(t)
	remote.SpoofIPList = "127.1.91.7\n127.1.91.8"
	remote.PeerSpoofIPList = "127.1.92.9"
	if e := db.SaveTunnel(s.db, &remote, true); e != nil {
		t.Fatal(e)
	}
	if e := s.manager.StartInstance(remote.ID); e != nil {
		t.Fatal(e)
	}
	ln, e := net.Listen("unix", s.manager.PoolSocket(remote.ID))
	if e != nil {
		t.Fatal(e)
	}
	defer ln.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			c, e := ln.Accept()
			if e != nil {
				return
			}
			var req l3.PoolControlRequest
			json.NewDecoder(c).Decode(&req)
			response := l3.PoolControlResponse{}
			switch req.Op {
			case "peer_sources":
				response.Sources = []string{"127.1.92.99"}
			case "status":
				response.Status = &l3.PoolRuntimeView{Pool: l3.PoolView{Active: "127.1.91.8"}}
			}
			json.NewEncoder(c).Encode(response)
			c.Close()
		}
	}()
	defer func() { ln.Close(); <-done }()
	out := benchmarkRequest(t, s, "GET", "/instances/1/benchmark-presets", nil)
	if out.Code != 200 {
		t.Fatal(out.Code, out.Body.String())
	}
	var p benchmarkPreset
	json.Unmarshal(out.Body.Bytes(), &p)
	if p.Source != "127.1.91.8" || p.PeerSource != "127.1.92.99" || !p.PeerLearned || !p.SourceVerified {
		t.Fatalf("did not use the active engine's effective state: %+v", p)
	}
	s.benchmark = benchmark.NewRunner(func(l3.Config, []byte) (benchmark.Wire, error) { return &presetWire{done: make(chan struct{})}, nil })
	// An explicitly chosen allowed source must remain fixed, even when another
	// source is currently active. No substitution or pool rotation in the test.
	request := map[string]any{"target_id": remote.ID, "local_ip": p.LocalIP, "peer_ip": p.PeerIP, "source": "127.1.91.7", "peer_source": p.PeerSource, "port_a": p.SendPort, "port_b": p.ReceivePort}
	out = benchmarkRequest(t, s, "POST", "/connection-test/receiver", request)
	if out.Code != 201 {
		t.Fatal("effective allowlist rejected before code generation", out.Code, out.Body.String())
	}
	var setup struct {
		Code string `json:"code"`
	}
	json.Unmarshal(out.Body.Bytes(), &setup)
	inv, e := benchmark.Decode(setup.Code)
	if e != nil || inv.SourceB != "127.1.91.7" || inv.SourceA != p.PeerSource {
		t.Fatal("explicit fixed source was changed", e, inv.SourceA, inv.SourceB)
	}
	var saved db.TunnelInstance
	s.db.First(&saved, remote.ID)
	if saved.PeerSpoofIPList != remote.PeerSpoofIPList || !saved.UpdatedAt.Equal(remote.UpdatedAt) {
		t.Fatal("benchmark mutated stored peer admission")
	}
}
