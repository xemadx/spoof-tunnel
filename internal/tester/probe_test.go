package tester

import (
	"encoding/binary"
	"errors"
	"net"
	"runtime"
	"syscall"
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
		startErr := runner.RunReceiver(cfg, ranges)
		if startErr != nil && runtime.GOOS == "linux" && !errors.Is(startErr, syscall.EPERM) {
			t.Fatal(startErr)
		}
		state := runner.State()
		if startErr != nil && (state.Status != "error" || state.Error == "") {
			t.Fatal("startup error was not published", startErr, state)
		}
		// Unprivileged CI can still check fresh codes and synchronous failure;
		// actual reception is exercised with CAP_NET_RAW in a private namespace.
		id := state.TestID
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

func TestTCPProbeIsPayloadFreeAndCorrelatesEverySequence(t *testing.T) {
	src, dst := net.ParseIP("198.18.0.1"), net.ParseIP("192.0.2.1")
	cfg := TesterConfig{TestID: "0123456789abcdef0123456789abcdef", Protocol: "tcp", DstPort: 19119, PacketCount: 1000}
	for i := uint32(0); i < 1000; i++ {
		p := buildProbe(cfg, src, dst, i)
		if len(p) != 40 || p[32] != 0x50 || p[33] != 2 || binary.BigEndian.Uint16(p[4:6]) == 0 {
			t.Fatalf("not a plain SYN at %d", i)
		}
		if n, err := tcpProbeSequence(p, 20, cfg.TestID, cfg.PacketCount); err != nil || n != i {
			t.Fatalf("sequence %d: %d %v", i, n, err)
		}
		if internetChecksum(p[:20]) != 0 || tcpChecksum(src.To4(), dst.To4(), p[20:]) != 0 {
			t.Fatal("bad checksum")
		}
		for _, offset := range []int{4, 12, 20, 24} {
			altered := append([]byte{}, p...)
			altered[offset] ^= 1
			if _, err := tcpProbeSequence(altered, 20, cfg.TestID, 1000); err == nil {
				t.Fatalf("accepted mutation at %d", offset)
			}
		}
		if _, err := tcpProbeSequence(p, 20, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 1000); err == nil {
			t.Fatal("accepted wrong code")
		}
		if _, err := tcpProbeSequence(p, 20, cfg.TestID, 999); err == nil {
			t.Fatal("accepted wrong count")
		}
	}
}

func TestTCPReceiverAcceptsPreviousPayloadFormat(t *testing.T) {
	src, dst := net.ParseIP("198.18.0.1"), net.ParseIP("192.0.2.1")
	id := "0123456789abcdef0123456789abcdef"
	p := append(BuildTCPSyn(src, dst, 19119), probePayload(id, src, 7, 10)...)
	if seq, err := tcpProbeSequence(p, 20, id, 10); err != nil || seq != 7 {
		t.Fatal(seq, err)
	}
}
