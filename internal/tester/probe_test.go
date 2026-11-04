package tester

import (
	"net"
	"testing"
)

func TestProbeCorrelationAndUniqueSequences(t *testing.T) {
	id := "0123456789abcdef0123456789abcdef"
	src := net.ParseIP("198.18.0.1")
	payload := probePayload(id, src, 7, 10)
	if n, e := probeSequence(payload, id, src, 10); e != nil || n != 7 {
		t.Fatal(n, e)
	}
	for _, bad := range []struct {
		id    string
		src   net.IP
		count int
	}{{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", src, 10}, {id, net.ParseIP("198.18.0.2"), 10}, {id, src, 7}} {
		if _, e := probeSequence(payload, bad.id, bad.src, bad.count); e == nil {
			t.Fatal("accepted unrelated probe")
		}
	}
	if _, e := probeSequence([]byte("ordinary ICMP traffic"), id, src, 10); e == nil {
		t.Fatal("accepted unrelated traffic")
	}
}

func TestReceiverCreatesFreshCodesAndSenderRequiresOne(t *testing.T) {
	ranges, _ := ParseIPRangesFromString("198.18.0.1")
	cfg := TesterConfig{Protocol: "icmp", Timeout: 1, PacketCount: 1, DstIP: "127.0.0.1"}
	if err := NewRunner().RunSender(cfg, ranges); err == nil {
		t.Fatal("sender accepted missing receiver code")
	}
	previous := ""
	for i := 0; i < 2; i++ {
		runner := NewRunner()
		runner.SetTmpDir(t.TempDir())
		if err := runner.RunReceiver(cfg, ranges); err != nil {
			t.Fatal(err)
		}
		id := runner.State().TestID
		runner.Stop()
		if !ValidTestID(id) || id == previous {
			t.Fatal("receiver did not create fresh code")
		}
		previous = id
	}
}

func TestProbeWireChecksums(t *testing.T) {
	src, dst := net.ParseIP("198.18.0.1"), net.ParseIP("192.0.2.1")
	for _, protocol := range []string{"icmp", "tcp"} {
		cfg := TesterConfig{TestID: "0123456789abcdef0123456789abcdef", Protocol: protocol, DstPort: 19119, PacketCount: 10}
		p := buildProbe(cfg, src, dst, 3)
		if internetChecksum(p[:20]) != 0 {
			t.Fatal("bad IP checksum")
		}
		if protocol == "icmp" && internetChecksum(p[20:]) != 0 {
			t.Fatal("bad ICMP checksum")
		}
		if protocol == "tcp" && tcpChecksum(src.To4(), dst.To4(), p[20:]) != 0 {
			t.Fatal("bad TCP checksum")
		}
	}
}
