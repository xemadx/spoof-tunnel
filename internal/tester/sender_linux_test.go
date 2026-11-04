//go:build linux

package tester

import (
	"net"
	"os"
	"syscall"
	"testing"
	"time"
)

func TestSenderDeliversEveryListedIP(t *testing.T) {
	if os.Getenv("SPOOF_TESTER_ISOLATED") != "1" {
		t.Skip("requires loopback-only network namespace")
	}
	for _, protocol := range []string{"icmp", "tcp"} {
		t.Run(protocol, func(t *testing.T) {
			ranges, err := ParseIPRangesFromString("198.18.0.1\n198.18.0.3\n198.18.0.5\n198.18.0.7\n198.18.0.9\n198.18.0.11")
			if err != nil {
				t.Fatal(err)
			}
			cfg := TesterConfig{TestID: "0123456789abcdef0123456789abcdef", Protocol: protocol, DstIP: "127.0.0.1", DstPort: 19119, Timeout: 10, PacketCount: 10, MaxPacketLoss: 0}
			receiver, sender := NewRunner(), NewRunner()
			receiver.SetTmpDir(t.TempDir())
			if err := receiver.RunReceiver(cfg, ranges); err != nil {
				t.Fatal(err)
			}
			defer receiver.Stop()
			time.Sleep(100 * time.Millisecond)
			if err := sender.RunSender(cfg, ranges); err != nil {
				t.Fatal(err)
			}
			defer sender.Stop()
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) && (sender.State().Status == "running" || receiver.State().RecvCount < 6) {
				time.Sleep(10 * time.Millisecond)
			}
			state := sender.State()
			if state.Status != "done" || state.ProcessedIPs != 6 || state.SentPackets != 60 || state.FailedPackets != 0 {
				t.Fatalf("sender: %+v", state)
			}
			received := receiver.LiveResults()
			if len(received) != 6 {
				t.Fatalf("received %d sources, want 6: %+v", len(received), received)
			}
			for _, result := range received {
				if result.Received != 10 || !result.Passed {
					t.Fatalf("missing packets: %+v", result)
				}
			}
		})
	}
}

func TestCancelledSenderDoesNotClaimCompletion(t *testing.T) {
	if os.Getenv("SPOOF_TESTER_ISOLATED") != "1" {
		t.Skip("requires isolated namespace")
	}
	ranges, _ := ParseIPRangesFromString("198.18.0.0/16")
	sender := NewRunner()
	cfg := TesterConfig{TestID: "0123456789abcdef0123456789abcdef", Protocol: "icmp", DstIP: "127.0.0.1", Timeout: 10, PacketCount: 1000}
	if err := sender.RunSender(cfg, ranges); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	sender.Stop()
	state := sender.State()
	if state.Status != "idle" || state.Progress == 100 || state.ProcessedIPs == state.TotalIPs {
		t.Fatalf("cancelled run claimed completion: %+v", state)
	}
}

func TestReceiverRejectsDuplicatesAndUnrelatedTraffic(t *testing.T) {
	if os.Getenv("SPOOF_TESTER_ISOLATED") != "1" {
		t.Skip("requires isolated namespace")
	}
	for _, protocol := range []string{"icmp", "tcp"} {
		t.Run(protocol, func(t *testing.T) {
			cfg := TesterConfig{TestID: "0123456789abcdef0123456789abcdef", Protocol: protocol, DstPort: 19119, Timeout: 10, PacketCount: 10, MaxPacketLoss: 0}
			ranges, _ := ParseIPRangesFromString("198.18.0.55")
			receiver := NewRunner()
			receiver.SetTmpDir(t.TempDir())
			if err := receiver.RunReceiver(cfg, ranges); err != nil {
				t.Fatal(err)
			}
			defer receiver.Stop()
			time.Sleep(100 * time.Millisecond)
			fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_RAW, syscall.IPPROTO_RAW)
			if err != nil {
				t.Fatal(err)
			}
			defer syscall.Close(fd)
			syscall.SetsockoptInt(fd, syscall.IPPROTO_IP, syscall.IP_HDRINCL, 1)
			src, dst := net.ParseIP("198.18.0.55"), net.ParseIP("127.0.0.1")
			send := func(p []byte) {
				t.Helper()
				if err := syscall.Sendto(fd, p, 0, &syscall.SockaddrInet4{Addr: [4]byte{127, 0, 0, 1}}); err != nil {
					t.Fatal(err)
				}
			}
			other := cfg
			other.TestID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			for i := 0; i < 10; i++ {
				send(buildProbe(cfg, src, dst, 0))
				send(buildProbe(other, src, dst, uint32(i)))
				if protocol == "tcp" {
					send(BuildTCPSyn(src, dst, cfg.DstPort))
				} else {
					send(BuildICMPEcho(src, dst, 777, 42))
				}
			}
			time.Sleep(100 * time.Millisecond)
			results := receiver.LiveResults()
			if len(results) != 1 || results[0].Received != 1 || results[0].Passed {
				t.Fatalf("false positive: %+v", results)
			}
			for i := uint32(1); i < 10; i++ {
				send(buildProbe(cfg, src, dst, i))
			}
			deadline := time.Now().Add(time.Second)
			for time.Now().Before(deadline) {
				results = receiver.LiveResults()
				if len(results) == 1 && results[0].Received == 10 && results[0].Passed {
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
			t.Fatalf("valid unique probes missing: %+v", results)
		})
	}
}
