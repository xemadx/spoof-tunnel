//go:build linux

package api

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/ParsaKSH/spoof-tunnel/internal/benchmark"
	"github.com/ParsaKSH/spoof-tunnel/panel/internal/auth"
	"github.com/ParsaKSH/spoof-tunnel/panel/internal/db"
)

// Real raw/AEAD benchmark, real HTTP/JWT/SQLite/process orchestration. The final
// process is a telemetry fixture, not a claim of Internet or TUN reachability.
func TestBenchmarkHTTPCreateBothEndsAfterExplicitApproval(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("raw sockets require root")
	}
	a, b := benchServer(t), benchServer(t)
	installBenchFixture(t, a)
	installBenchFixture(t, b)
	call := func(s *Server, path string, data any) *httptest.ResponseRecorder {
		t.Helper()
		var user db.User
		s.db.First(&user)
		if user.ID == 0 {
			user = db.User{Username: "bench", PasswordHash: "unused"}
			s.db.Create(&user)
		}
		token, _ := auth.GenerateToken(user.ID, user.Username)
		body, _ := json.Marshal(data)
		req := httptest.NewRequest("POST", "/test/api/connection-test/"+path, bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		out := httptest.NewRecorder()
		s.Router().ServeHTTP(out, req)
		return out
	}
	receiver := call(b, "receiver", map[string]any{"local_ip": "127.0.0.91", "peer_ip": "127.0.0.92", "source": "127.1.91.1", "peer_source": "127.1.92.1", "port_a": 39791, "port_b": 39792, "allow_apply": true})
	if receiver.Code != 201 {
		t.Fatal(receiver.Code, receiver.Body.String())
	}
	var setup struct {
		Code string `json:"code"`
	}
	json.Unmarshal(receiver.Body.Bytes(), &setup)
	start := call(a, "start", benchmarkDraft{Code: setup.Code, Name: "measured", Subnet: "10.88.0.0/30", Options: benchmark.Options{RateMbps: 1, Seconds: 2}})
	if start.Code != 202 {
		t.Fatal(start.Code, start.Body.String())
	}
	deadline := time.Now().Add(4 * time.Minute)
	for a.benchmark.Status().Status != "done" && time.Now().Before(deadline) {
		time.Sleep(250 * time.Millisecond)
	}
	v := a.benchmark.Status()
	if v.Recommended < 0 {
		t.Fatalf("benchmark failed: %+v", v)
	}
	for _, s := range []*Server{a, b} {
		var count int64
		s.db.Model(&db.TunnelInstance{}).Count(&count)
		if count != 0 {
			t.Fatal("benchmark modified configuration before approval")
		}
	}
	preview := call(a, "preview", map[string]any{"index": v.Recommended})
	if preview.Code != 200 {
		t.Fatal(preview.Code, preview.Body.String())
	}
	var p struct {
		Token string `json:"token"`
	}
	json.Unmarshal(preview.Body.Bytes(), &p)
	for _, s := range []*Server{a, b} {
		var count int64
		s.db.Model(&db.TunnelInstance{}).Count(&count)
		if count != 0 {
			t.Fatal("preview persisted an unapproved connection")
		}
	}
	apply := call(a, "apply", map[string]any{"token": p.Token})
	if apply.Code != 202 {
		t.Fatal(apply.Code, apply.Body.String())
	}
	deadline = time.Now().Add(45 * time.Second)
	for a.benchmark.Status().Status == "applying" && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if v = a.benchmark.Status(); !v.Applied {
		t.Fatalf("apply failed: %+v", v)
	}
	if !b.benchmark.Status().Applied {
		t.Fatal("receiving UI did not synchronize applied state")
	}
	var local, remote db.TunnelInstance
	a.db.First(&local)
	b.db.First(&remote)
	if local.PSK != remote.PSK || local.TunPeer != localIPFromCIDR(remote.TunAddress) || local.SendTransport != remote.RecvTransport || local.RecvTransport != remote.SendTransport {
		t.Fatal("created pair is not complementary")
	}
	for _, s := range []*Server{a, b} {
		var count int64
		s.db.Model(&db.Setting{}).Where("key LIKE ?", "benchmark-apply-%").Count(&count)
		if count != 0 {
			t.Fatal("finalization retained crash journal")
		}
	}
}
