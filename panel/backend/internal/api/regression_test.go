package api

import (
	"encoding/json"
	"github.com/ParsaKSH/spoof-tunnel/panel/internal/auth"
	"github.com/ParsaKSH/spoof-tunnel/panel/internal/db"
	"github.com/ParsaKSH/spoof-tunnel/panel/internal/manager"
	"github.com/gorilla/websocket"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestWebsocketAuthenticationDoesNotPutJWTInURL(t *testing.T) {
	dir := t.TempDir()
	database, err := db.InitDB(filepath.Join(dir, "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { d, _ := database.DB(); d.Close() }()
	user := db.User{Username: "admin", PasswordHash: "unused"}
	database.Create(&user)
	token, _ := auth.GenerateToken(user.ID, user.Username)
	server := NewServer(database, manager.NewManager(database, "unused", dir), "/prefix", "test")
	defer server.Close()
	httpServer := httptest.NewServer(server.Router())
	defer httpServer.Close()
	request, _ := http.NewRequest("GET", httpServer.URL+"/prefix/api/instances/1/logs?token="+token, nil)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 401 {
		t.Fatal("accepted query token")
	}
	dialer := websocket.Dialer{Subprotocols: []string{"spoof-logs", token}}
	connection, _, err := dialer.Dial("ws"+strings.TrimPrefix(httpServer.URL, "http")+"/prefix/api/instances/1/logs", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if connection.Subprotocol() != "spoof-logs" {
		t.Fatal("server echoed an unexpected protocol")
	}
}
func TestStaleConfigCannotOverwriteNewPool(t *testing.T) {
	dir := t.TempDir()
	database, err := db.InitDB(filepath.Join(dir, "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { d, _ := database.DB(); d.Close() }()
	user := db.User{Username: "admin", PasswordHash: "unused"}
	database.Create(&user)
	token, _ := auth.GenerateToken(user.ID, user.Username)
	server := NewServer(database, manager.NewManager(database, "unused", dir), "/prefix", "test")
	defer server.Close()
	pair := pairRequest{Name: "test", Engine: "tun", LocalIP: "192.0.2.1", RemoteIP: "192.0.2.2", LocalSpoof: "198.51.100.1", RemoteSpoof: "198.51.100.2", Upload: "icmp", Download: "icmp", LocalPort: 5001, RemotePort: 8090}
	local, _, err := buildPair(pair)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.SaveTunnel(database, &local, true); err != nil {
		t.Fatal(err)
	}
	old, _ := json.Marshal(local)
	local.SpoofIPList = "198.51.100.3"
	if err = db.SaveTunnel(database, &local, false); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("PUT", "/prefix/api/instances/1", strings.NewReader(string(old)))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.Router().ServeHTTP(response, request)
	if response.Code != 409 {
		t.Fatalf("stale update status=%d body=%s", response.Code, response.Body)
	}
	var saved db.TunnelInstance
	database.First(&saved, local.ID)
	if saved.SpoofIPList != "198.51.100.3" {
		t.Fatal("lost updated pool")
	}
}

func TestTesterStatusIncludesSenderCounters(t *testing.T) {
	dir := t.TempDir()
	database, err := db.InitDB(filepath.Join(dir, "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { d, _ := database.DB(); d.Close() }()
	user := db.User{Username: "admin", PasswordHash: "unused"}
	database.Create(&user)
	token, _ := auth.GenerateToken(user.ID, user.Username)
	server := NewServer(database, manager.NewManager(database, "unused", dir), "/prefix", "test")
	defer server.Close()
	request := httptest.NewRequest("GET", "/prefix/api/tester/status", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	server.Router().ServeHTTP(response, request)
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	var state map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"processed_ips", "sent_packets", "failed_packets", "rejected_probes", "duplicate_probes"} {
		if string(state[key]) != "0" {
			t.Fatalf("counter %s missing or wrong: %s", key, response.Body)
		}
	}
}
