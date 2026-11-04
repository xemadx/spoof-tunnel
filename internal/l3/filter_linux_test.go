//go:build linux

package l3

import (
	"os"
	"testing"
	"time"
)

func TestSourceFilterHasSeparateDiagnostics(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires raw sockets")
	}
	cfg := Config{LocalIP: "127.0.0.1", PeerIP: "127.0.0.1", SpoofSource: "127.0.0.4", PeerSpoof: "127.0.0.3", Send: "udp", Receive: "udp", SendPort: 8090, ReceivePort: 5001}
	receiver, err := NewPacketConn(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer receiver.Close()
	receiver.recv.SetReadDeadline(time.Now().Add(time.Second))
	cfg.PeerSpoof = "127.0.0.4"
	cfg.SendPort = 5001
	cfg.ReceivePort = 8090
	cfg.SpoofSource = "127.0.0.2"
	bad, err := NewPacketConn(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer bad.Close()
	cfg.SpoofSource = "127.0.0.3"
	good, err := NewPacketConn(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer good.Close()
	if err := bad.Send([]byte("ignored")); err != nil {
		t.Fatal(err)
	}
	if err := good.Send([]byte("accepted")); err != nil {
		t.Fatal(err)
	}
	data, err := receiver.Receive()
	if err != nil || string(data) != "accepted" {
		t.Fatal(string(data), err)
	}
	if receiver.rejected.Load() < 1 || receiver.rejectedSource.Load() != "127.0.0.2" {
		t.Fatal("missing rejected-source diagnostics")
	}
}
