//go:build linux

package l3

import (
	"os"
	"os/exec"
	"testing"
)

// Run only inside a dedicated network namespace; never modify host firewall
// rules as a side effect of the ordinary unit-test command.
func TestBackscatterIsolated(t *testing.T) {
	if os.Getenv("SPOOF_BACKSCATTER_ISOLATED") != "1" {
		t.Skip("requires isolated network namespace")
	}
	ping := func(id string, want bool) {
		t.Helper()
		out, err := exec.Command("ping", "-n", "-c", "1", "-W", "1", "-e", id, "127.0.0.1").CombinedOutput()
		if (err == nil) != want {
			t.Fatalf("ping id=%s: expected success=%v: %v\n%s", id, want, err, out)
		}
	}
	first := StartBackscatterSuppression("icmp", 5001)
	defer first.Stop()
	if len(first.rules) != 1 {
		t.Fatal("ICMP suppression rule was not installed")
	}
	ping("5002", true)
	ping("5001", false)
	second := StartBackscatterSuppression("icmp", 5001)
	defer second.Stop()
	first.Stop()
	ping("5001", false)
	second.Stop()
	ping("5001", true)
	// A dead process's quoted rule comment must be removed correctly.
	rule := []string{"-A", "OUTPUT", "-p", "icmp", "--icmp-type", "echo-reply", "-m", "comment", "--comment", "spoof-tunnel-2147483647-stale", "-j", "DROP"}
	if out, err := exec.Command("iptables", rule...).CombinedOutput(); err != nil {
		t.Fatalf("install stale rule: %v %s", err, out)
	}
	cleanupStaleRules()
	ping("5002", true)
}
