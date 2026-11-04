//go:build linux

package tester

import (
	"net"
	"os"
	"testing"
	"time"
)

func TestLastIPv4StopsAtEnd(t *testing.T) {
	r, e := ParseIPRangesFromString("255.255.255.255")
	if e != nil {
		t.Fatal(e)
	}
	var got []string
	r.IterateIPs(func(ip net.IP) bool { got = append(got, ip.String()); return len(got) < 2 })
	if len(got) != 1 {
		t.Fatalf("one-IP range iterated beyond end: %v", got)
	}
}
func TestRestartJoinsPreviousReceiver(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires CAP_NET_RAW")
	}
	r := NewRunner()
	r.SetTmpDir(t.TempDir())
	ranges, _ := ParseIPRangesFromString("192.0.2.99")
	cfg := TesterConfig{TestID: "0123456789abcdef0123456789abcdef", Mode: "receiver", Protocol: "icmp", Timeout: 10, PacketCount: 1}
	if e := r.RunReceiver(cfg, ranges); e != nil {
		t.Fatal(e)
	}
	time.Sleep(100 * time.Millisecond)
	r.Stop()
	if e := r.RunReceiver(cfg, ranges); e != nil {
		t.Fatal(e)
	}
	defer r.Stop()
	time.Sleep(1500 * time.Millisecond)
	s := r.State()
	if s.Status != "running" {
		t.Fatalf("old stopped receiver overwrote new receiver state: %s", s.Status)
	}
}
