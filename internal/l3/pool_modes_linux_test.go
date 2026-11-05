//go:build linux

package l3

import (
	"errors"
	"net"
	"strings"
	"testing"
)

func TestEverySmartModeGatesDataButKeepsControlAvailable(t *testing.T) {
	for _, mode := range []string{"adaptive", "round_robin", "failover"} {
		t.Run(mode, func(t *testing.T) {
			pool, _ := confirmedPool(t, mode)
			conn := &PacketConn{cfg: Config{PoolPolicy: PoolPolicy{Mode: mode}, AutoPeerSync: true}, pool: pool, closed: true}
			if err := conn.SendData(nil); err == nil || !strings.Contains(err.Error(), "waiting for verified peer") {
				t.Fatal("data sent before remote ready", err)
			}
			if err := conn.Send(nil); !errors.Is(err, net.ErrClosed) {
				t.Fatal("readiness blocked control/discovery", err)
			}
			conn.peerReady.Store(true)
			if err := conn.SendData(nil); err == nil || !strings.Contains(err.Error(), "waiting for verified peer") {
				t.Fatal("data sent before manifest accepted", err)
			}
			conn.peerAccepted.Store(true)
			if err := conn.SendData(nil); !errors.Is(err, net.ErrClosed) {
				t.Fatal("verified path did not reach carrier", err)
			}
			conn.cfg.AutoPeerSync = false
			conn.peerAccepted.Store(false)
			if err := conn.SendData(nil); !errors.Is(err, net.ErrClosed) {
				t.Fatal("manual allowlist required optional manifest", err)
			}
			conn.pool = NewSourcePool([]net.IP{net.ParseIP("192.0.2.1")}, conn.cfg.PoolPolicy)
			if err := conn.SendData(nil); err == nil || !strings.Contains(err.Error(), "no verified healthy source") {
				t.Fatal("unknown source carried data", err)
			}
		})
	}
	conn := &PacketConn{cfg: Config{PoolPolicy: PoolPolicy{Mode: "legacy"}, AutoPeerSync: true}, pool: NewSourcePool([]net.IP{net.ParseIP("192.0.2.1")}, PoolPolicy{Mode: "legacy"}), closed: true}
	if err := conn.SendData(nil); !errors.Is(err, net.ErrClosed) {
		t.Fatal("legacy behavior silently changed", err)
	}
}
