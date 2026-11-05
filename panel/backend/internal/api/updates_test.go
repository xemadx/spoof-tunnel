package api

import (
	"github.com/ParsaKSH/spoof-tunnel/panel/internal/db"
	"github.com/ParsaKSH/spoof-tunnel/panel/internal/manager"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpdateEndpointsRequireAuthentication(t *testing.T) {
	dir := t.TempDir()
	database, err := db.InitDB(filepath.Join(dir, "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { d, _ := database.DB(); d.Close() }()
	server := NewServer(database, manager.NewManager(database, "unused", dir), "/prefix", "v0.4.1")
	defer server.Close()
	for _, request := range [][2]string{{"GET", "/updates"}, {"POST", "/updates/check"}, {"POST", "/updates/install"}} {
		out := httptest.NewRecorder()
		req := httptest.NewRequest(request[0], "/prefix/api"+request[1], strings.NewReader(`{"version":"v0.4.2"}`))
		req.Header.Set("Content-Type", "application/json")
		server.Router().ServeHTTP(out, req)
		if out.Code != 401 {
			t.Fatal("unauthenticated updater access", request, out.Code)
		}
	}
	out := httptest.NewRecorder()
	server.Router().ServeHTTP(out, httptest.NewRequest("GET", "/prefix/api/health", nil))
	if out.Code != 200 || !strings.Contains(out.Body.String(), "v0.4.1") {
		t.Fatal("version health check failed", out.Code, out.Body)
	}
}
