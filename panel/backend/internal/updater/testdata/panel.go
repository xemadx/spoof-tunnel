// A real API/SQLite process used to test binary replacement and rollback without
// touching host systemd services or requiring the frontend build during go test.
package main

import (
	"fmt"
	"github.com/ParsaKSH/spoof-tunnel/panel/internal/api"
	"github.com/ParsaKSH/spoof-tunnel/panel/internal/auth"
	"github.com/ParsaKSH/spoof-tunnel/panel/internal/db"
	"github.com/ParsaKSH/spoof-tunnel/panel/internal/manager"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

var version = "dev"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		if strings.HasSuffix(os.Args[0], "spoof-l3") {
			fmt.Print("spoof-l3 ")
		}
		fmt.Println(version)
		return
	}
	directory := os.Getenv("FIXTURE_DATA")
	database, err := db.InitDB(filepath.Join(directory, "panel.db"))
	if err != nil {
		panic(err)
	}
	if len(os.Args) > 1 && os.Args[1] == "--seed" {
		hash, _ := auth.HashPassword("test-only-update-password")
		database.Create(&db.User{Username: "update-test", PasswordHash: hash})
		for k, v := range map[string]string{"panel_port": os.Getenv("FIXTURE_PORT"), "web_path": "fixture", "jwt_secret": "isolated-persistent-update-test-secret"} {
			database.Create(&db.Setting{Key: k, Value: v})
		}
		inst := db.TunnelInstance{Name: "preserved-connection", Mode: "local", Engine: "tun", LocalIP: "192.0.2.1", RemoteAddr: "192.0.2.2", SpoofIP: "198.51.100.1", PeerSpoofIP: "198.51.100.2", SendTransport: "icmp", RecvTransport: "icmp", RemotePort: 8090, RecvPort: 5001, TunInterface: "spupdate", TunAddress: "10.77.0.1/30", TunPeer: "10.77.0.2", TunMTU: 1280, PSK: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}
		if err := db.SaveTunnel(database, &inst, true); err != nil {
			panic(err)
		}
		database.Create(&db.PoolFile{Kind: "file", InstanceID: inst.ID, Name: "preserved.txt", Count: 1, Data: []byte{198, 51, 100, 3}})
		return
	}
	var setting db.Setting
	database.Where("key = ?", "jwt_secret").First(&setting)
	auth.SetSecret(setting.Value)
	setting = db.Setting{}
	database.Where("key = ?", "panel_port").First(&setting)
	manager := manager.NewManager(database, "unused", directory)
	server := api.NewServer(database, manager, "/fixture", version)
	if err = http.ListenAndServe("127.0.0.1:"+setting.Value, server.Router()); err != nil {
		panic(err)
	}
}
