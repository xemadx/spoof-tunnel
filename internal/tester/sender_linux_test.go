//go:build linux

package tester

import (
	"os"
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
			cfg := TesterConfig{Protocol: protocol, DstIP: "127.0.0.1", DstPort: 19119, Timeout: 10, PacketCount: 10, MaxPacketLoss: 0}
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
	cfg := TesterConfig{Protocol: "icmp", DstIP: "127.0.0.1", Timeout: 10, PacketCount: 1000}
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
