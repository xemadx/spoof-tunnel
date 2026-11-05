package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/ParsaKSH/spoof-tunnel/panel/internal/auth"
	"github.com/ParsaKSH/spoof-tunnel/panel/internal/db"
	"github.com/ParsaKSH/spoof-tunnel/panel/internal/manager"
	"io"
	"mime/multipart"
	"net"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNamedPoolsLargeImportSelectionAndCompactConfig(t *testing.T) {
	dir := t.TempDir()
	database, err := db.InitDB(filepath.Join(dir, "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { d, _ := database.DB(); d.Close() }()
	user := db.User{Username: "pool-test", PasswordHash: "unused"}
	database.Create(&user)
	token, _ := auth.GenerateToken(user.ID, user.Username)
	server := NewServer(database, manager.NewManager(database, "unused", dir), "/prefix", "test")
	defer server.Close()
	local, _, err := buildPair(pairRequest{Name: "pools", Engine: "tun", LocalIP: "192.0.2.1", RemoteIP: "192.0.2.2", LocalSpoof: "203.0.113.1", RemoteSpoof: "203.0.113.2", Upload: "icmp", Download: "icmp", LocalPort: 5001, RemotePort: 8090})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.SaveTunnel(database, &local, true); err != nil {
		t.Fatal(err)
	}
	call := func(method, path string, body []byte, kind string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/prefix/api"+path, bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", kind)
		out := httptest.NewRecorder()
		server.Router().ServeHTTP(out, req)
		return out
	}
	upload := func(name string, start, count int) db.PoolFile {
		var body bytes.Buffer
		w := multipart.NewWriter(&body)
		file, _ := w.CreateFormFile("file", name)
		for i := start; i < start+count; i++ {
			v := uint32(198)<<24 | uint32(51)<<16
			v += uint32(i)
			fmt.Fprintf(file, "%d.%d.%d.%d\n", byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
		}
		w.Close()
		out := call("POST", "/instances/1/pools/upload", body.Bytes(), w.FormDataContentType())
		if out.Code != 201 {
			t.Fatalf("upload failed: %d %s", out.Code, out.Body)
		}
		var row db.PoolFile
		if json.Unmarshal(out.Body.Bytes(), &row) != nil || row.Count != count {
			t.Fatal("wrong file count")
		}
		return row
	}
	a := upload("اول.txt", 0, 160000)
	b := upload("دوم.txt", 100000, 100000)
	settings, _ := json.Marshal(map[string]any{"ids": []uint{a.ID, b.ID}, "mode": "adaptive", "probe_pps": 25, "auto_peer_sync": true, "updated_at": local.UpdatedAt})
	if len(settings) > 1024 {
		t.Fatal("settings embedded large pool")
	}
	out := call("PUT", "/instances/1/pools/settings", settings, "application/json")
	if out.Code != 200 {
		t.Fatalf("settings failed %d: %s", out.Code, out.Body)
	}
	var current db.TunnelInstance
	database.First(&current, local.ID)
	if n := len(strings.Fields(current.SpoofIPList)); n != 200000 {
		t.Fatalf("overlap not deduplicated: %d", n)
	}
	for _, path := range []string{"/instances/1?compact=1", "/instances", "/instances/1/pools"} {
		out = call("GET", path, nil, "application/json")
		if out.Code != 200 || out.Body.Len() > 10000 {
			t.Fatalf("large polling response %s: status=%d bytes=%d", path, out.Code, out.Body.Len())
		}
	}
	// A file from another connection cannot be selected, and a rejected request
	// must not alter enabled files or the compiled pool.
	alien := db.PoolFile{InstanceID: 999, Name: "foreign.txt", Data: []byte{1, 1, 1, 1}, Count: 1}
	database.Create(&alien)
	bad, _ := json.Marshal(map[string]any{"ids": []uint{alien.ID}, "mode": "adaptive", "probe_pps": 25, "updated_at": current.UpdatedAt})
	out = call("PUT", "/instances/1/pools/settings", bad, "application/json")
	if out.Code != 400 {
		t.Fatal("cross-instance pool accepted", out.Code)
	}
	var after db.TunnelInstance
	database.First(&after, local.ID)
	if after.SpoofIPList != current.SpoofIPList {
		t.Fatal("invalid request partially replaced pool")
	}
	// A stale editor cannot overwrite a newer file selection.
	out = call("PUT", "/instances/1/pools/settings", settings, "application/json")
	if out.Code != 409 {
		t.Fatal("stale pool settings accepted", out.Code)
	}
	out = call("DELETE", fmt.Sprintf("/instances/1/pools/%d", a.ID), nil, "application/json")
	if out.Code != 409 {
		t.Fatal("active pool file deleted", out.Code)
	}
}

func TestDefaultManualGroupStaysIndependentFromTXTFiles(t *testing.T) {
	dir := t.TempDir()
	database, err := db.InitDB(filepath.Join(dir, "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { d, _ := database.DB(); d.Close() }()
	user := db.User{Username: "default-test", PasswordHash: "unused"}
	database.Create(&user)
	token, _ := auth.GenerateToken(user.ID, user.Username)
	server := NewServer(database, manager.NewManager(database, "unused", dir), "/prefix", "test")
	defer server.Close()
	inst, _, err := buildPair(pairRequest{Name: "default", Engine: "tun", LocalIP: "192.0.2.1", RemoteIP: "192.0.2.2", LocalSpoof: "203.0.113.1", RemoteSpoof: "203.0.113.2", Upload: "icmp", Download: "icmp", LocalPort: 5001, RemotePort: 8090})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.SaveTunnel(database, &inst, true); err != nil {
		t.Fatal(err)
	}
	call := func(method, path string, body []byte, kind string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/prefix/api"+path, bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", kind)
		out := httptest.NewRecorder()
		server.Router().ServeHTTP(out, req)
		return out
	}
	save := func(content string) {
		database.First(&inst, inst.ID)
		var b bytes.Buffer
		w := multipart.NewWriter(&b)
		f, _ := w.CreateFormFile("file", "manual.txt")
		f.Write([]byte(content))
		w.WriteField("updated_at", inst.UpdatedAt.Format("2006-01-02T15:04:05.999999999Z07:00"))
		w.Close()
		out := call("POST", "/instances/1/pools/default", b.Bytes(), w.FormDataContentType())
		if out.Code != 200 {
			t.Fatalf("default save %d %s", out.Code, out.Body)
		}
	}
	var defaults []db.PoolFile
	database.Where("instance_id = ? AND kind = ?", inst.ID, "default").Find(&defaults)
	if len(defaults) != 1 || defaults[0].Count != 1 || !defaults[0].Enabled {
		t.Fatal("default group was not created")
	}
	// Preserve the old inline endpoint when no TXT group is active.
	out := call("PUT", "/instances/1/spoof-ips", []byte(`{"content":"203.0.113.9"}`), "application/json")
	if out.Code != 200 {
		t.Fatalf("legacy manual API broke: %d %s", out.Code, out.Body)
	}
	save("203.0.113.5\n203.0.113.6\n203.0.113.5")
	var row db.PoolFile
	database.First(&row, defaults[0].ID)
	if row.Count != 2 || row.Kind != "default" {
		t.Fatal("manual dedup/group failed")
	}
	file := poolFile("فایل.txt", inst.ID, []net.IP{net.ParseIP("198.51.100.55")})
	database.Create(&file)
	database.First(&inst, inst.ID)
	settings, _ := json.Marshal(map[string]any{"ids": []uint{file.ID}, "mode": "adaptive", "probe_pps": 25, "updated_at": inst.UpdatedAt})
	out = call("PUT", "/instances/1/pools/settings", settings, "application/json")
	if out.Code != 200 {
		t.Fatal("TXT selection failed", out.Body)
	}
	save("203.0.113.8")
	database.First(&inst, inst.ID)
	if inst.SpoofIPList != "198.51.100.55" {
		t.Fatal("editing unselected default changed the active TXT group")
	}
	database.First(&row, defaults[0].ID)
	if row.Enabled || row.Count != 1 {
		t.Fatal("default editor silently activated group")
	}
	save("")
	database.First(&row, defaults[0].ID)
	if row.Count != 0 || row.Enabled {
		t.Fatal("inactive default could not be cleared")
	}
	out = call("DELETE", fmt.Sprintf("/instances/1/pools/%d", row.ID), nil, "application/json")
	if out.Code != 409 {
		t.Fatal("default group was deletable")
	}
	database.First(&file, file.ID)
	if file.Count != 1 || string(file.Data) != string([]byte{198, 51, 100, 55}) {
		t.Fatal("default edit modified TXT storage")
	}
}

func TestSlowPoolBodyDoesNotBlockConnectionStop(t *testing.T) {
	for _, endpoint := range []string{"upload", "default"} {
		t.Run(endpoint, func(t *testing.T) {
			dir := t.TempDir()
			database, err := db.InitDB(filepath.Join(dir, "panel.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { d, _ := database.DB(); d.Close() }()
			user := db.User{Username: "slow-test", PasswordHash: "unused"}
			database.Create(&user)
			token, _ := auth.GenerateToken(user.ID, user.Username)
			server := NewServer(database, manager.NewManager(database, "unused", dir), "/prefix", "test")
			defer server.Close()
			inst, _, err := buildPair(pairRequest{Name: "slow", Engine: "tun", LocalIP: "192.0.2.1", RemoteIP: "192.0.2.2", LocalSpoof: "203.0.113.1", RemoteSpoof: "203.0.113.2", Upload: "icmp", Download: "icmp", LocalPort: 5001, RemotePort: 8090})
			if err != nil {
				t.Fatal(err)
			}
			if err = db.SaveTunnel(database, &inst, true); err != nil {
				t.Fatal(err)
			}
			var payload bytes.Buffer
			form := multipart.NewWriter(&payload)
			f, _ := form.CreateFormFile("file", "slow.txt")
			f.Write([]byte("198.51.100.10\n"))
			form.WriteField("updated_at", inst.UpdatedAt.Format(time.RFC3339Nano))
			form.Close()
			reader, writer := io.Pipe()
			firstRead := make(chan struct{})
			release := make(chan struct{})
			done := make(chan struct{})
			var once sync.Once
			unlock := func() { once.Do(func() { close(release) }) }
			defer func() { unlock(); writer.Close(); reader.Close(); <-done }()
			req := httptest.NewRequest("POST", "/prefix/api/instances/1/pools/"+endpoint, reader)
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Content-Type", form.FormDataContentType())
			response := httptest.NewRecorder()
			go func() { server.Router().ServeHTTP(response, req); close(done) }()
			raw := payload.Bytes()
			go func() {
				writer.Write(raw[:len(raw)/2])
				close(firstRead)
				<-release
				writer.Write(raw[len(raw)/2:])
				writer.Close()
			}()
			select {
			case <-firstRead:
			case <-time.After(2 * time.Second):
				t.Fatal("handler did not begin reading body")
			}
			stop := httptest.NewRequest("POST", "/prefix/api/instances/1/stop", nil)
			stop.Header.Set("Authorization", "Bearer "+token)
			stopped := httptest.NewRecorder()
			stopDone := make(chan struct{})
			go func() { server.Router().ServeHTTP(stopped, stop); close(stopDone) }()
			select {
			case <-stopDone:
				if stopped.Code != 200 {
					t.Fatal("stop failed", stopped.Code)
				}
			case <-time.After(time.Second):
				t.Fatal("slow upload held the global Start/Stop lock")
			}
			unlock()
			select {
			case <-done:
				if response.Code != 200 && response.Code != 201 {
					t.Fatalf("upload failed %d %s", response.Code, response.Body)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("upload did not finish")
			}
		})
	}
}
