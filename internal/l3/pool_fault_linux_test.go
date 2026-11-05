//go:build linux

package l3

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestPoolRawBadSourceQuarantineAndCheckpoint(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires raw sockets")
	}
	dir := t.TempDir()
	a := Config{Role: "local", LocalIP: "127.0.0.3", PeerIP: "127.0.0.4", SpoofSources: []string{"127.1.0.10", "127.1.0.11"}, PeerSpoof: "127.9.0.1", Send: "icmp", Receive: "icmp", SendPort: 20111, ReceivePort: 20112, PSK: base64.StdEncoding.EncodeToString(make([]byte, 32)), MTU: 1280, PoolPolicy: PoolPolicy{Mode: "adaptive", ProbePPS: 100}, AutoPeerSync: true, ControlSocket: filepath.Join(dir, "a.sock"), PoolStateFile: filepath.Join(dir, "a.json")}
	b := a
	b.Role = "remote"
	b.LocalIP, b.PeerIP = a.PeerIP, a.LocalIP
	b.SendPort, b.ReceivePort = a.ReceivePort, a.SendPort
	b.SpoofSources = []string{"127.2.0.10", "127.2.0.11"}
	b.ControlSocket = ""
	b.PoolStateFile = ""
	ca, err := NewPacketConn(a)
	if err != nil {
		t.Fatal(err)
	}
	cb, err := NewPacketConn(b)
	if err != nil {
		ca.Close()
		t.Fatal(err)
	}
	coa, _ := NewCodec(a.PSK, a.Role)
	cob, _ := NewCodec(b.PSK, b.Role)
	ra, rb := newPoolRuntime(a, ca, coa), newPoolRuntime(b, cb, cob)
	ctx, cancel := context.WithCancel(context.Background())
	var readers sync.WaitGroup
	defer func() {
		cancel()
		ca.Close()
		cb.Close()
		ra.close()
		rb.close()
		readers.Wait()
	}()
	if err = ra.start(ctx); err != nil {
		t.Fatal(err)
	}
	if err = rb.start(ctx); err != nil {
		t.Fatal(err)
	}
	for _, x := range []struct {
		c     *PacketConn
		codec *Codec
		r     *poolRuntime
		drop  string
	}{{ca, coa, ra, ""}, {cb, cob, rb, "127.1.0.11"}} {
		readers.Add(1)
		go func(c *PacketConn, codec *Codec, r *poolRuntime, drop string) {
			defer readers.Done()
			for {
				frame, source, _, err := c.receivePacket(true)
				if err != nil {
					return
				}
				if source == drop {
					continue
				}
				kind, data, err := codec.Open(frame)
				if err != nil {
					continue
				}
				if kind == 4 {
					r.receive(data, source)
				}
				if kind == 5 {
					r.receiveProbe(data, source)
				}
			}
		}(x.c, x.codec, x.r, x.drop)
	}
	deadline := time.Now().Add(55 * time.Second)
	for time.Now().Before(deadline) {
		view := ra.snapshot(0, 50, "")
		if view.Pool.Quarantined == 1 && view.Pool.Healthy == 1 && view.OurSourcesAccepted {
			for i := 0; i < 20; i++ {
				ip, err := ca.pool.Select(false)
				if err != nil || ip.String() != "127.1.0.10" {
					t.Fatal("bad source carried data", ip, err)
				}
			}
			response, err := PoolControl(a.ControlSocket, PoolControlRequest{Op: "status", Filter: "quarantined", Limit: 50})
			if err != nil || response.Status == nil || len(response.Status.Pool.Items) != 1 {
				t.Fatal("IPC quality view failed", err)
			}
			raw, err := os.ReadFile(a.PoolStateFile)
			if err != nil {
				time.Sleep(30 * time.Millisecond)
				continue
			}
			var disk poolDisk
			if json.Unmarshal(raw, &disk) != nil || disk.Quarantine["127.1.0.11"] < time.Now().Add(590*time.Second).Unix() {
				time.Sleep(30 * time.Millisecond)
				continue
			}
			restored := NewSourcePool(ca.pool.ips, a.PoolPolicy)
			restored.Restore(disk.Quarantine, time.Now())
			if restored.View(0, 50, "").Quarantined != 1 {
				t.Fatal("restart lost quarantine")
			}
			return
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatalf("source-specific quarantine failed: %+v", ra.snapshot(0, 50, ""))
}
