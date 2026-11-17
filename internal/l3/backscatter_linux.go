//go:build linux

package l3

import (
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// BackscatterSuppression manages listeners and iptables drop rules to prevent
// kernel backscatter (ICMP echo reply, TCP RST on closed ports, and ICMP
// port unreachable) for spoofed packets handled by raw sockets.
type BackscatterSuppression struct {
	mu       sync.Mutex
	rules    [][]string
	dummyUDP net.PacketConn
}

var suppressionID atomic.Uint64

// cleanupStaleRules scans the OUTPUT chain for rules added by spoof-tunnel
// processes that have died (e.g. killed via SIGKILL), cleaning them up.
func cleanupStaleRules() {
	out, err := exec.Command("iptables", "-w", "5", "-S", "OUTPUT").Output()
	if err != nil {
		return
	}
	re := regexp.MustCompile(`spoof-tunnel-(\d+)-`)
	for _, line := range strings.Split(string(out), "\n") {
		matches := re.FindStringSubmatch(line)
		if len(matches) == 2 {
			pid, err := strconv.Atoi(matches[1])
			if err == nil && pid > 0 {
				if _, err := os.Stat(fmt.Sprintf("/proc/%d", pid)); os.IsNotExist(err) {
					// Process is dead, remove stale rule
					parts := strings.Fields(line)
					if len(parts) > 2 && parts[0] == "-A" && parts[1] == "OUTPUT" {
						// iptables -S quotes comment values; exec does not invoke a
						// shell to remove those quotes for us.
						for i := 2; i < len(parts); i++ {
							parts[i] = strings.Trim(parts[i], "\"")
						}
						delArgs := append([]string{"-w", "5", "-D", "OUTPUT"}, parts[2:]...)
						_ = exec.Command("iptables", delArgs...).Run()
					}
				}
			}
		}
	}
}

// StartBackscatterSuppression configures suppression rules for the given
// carrier protocol ("icmp", "tcp", "udp") and port.
func StartBackscatterSuppression(proto string, port int) *BackscatterSuppression {
	cleanupStaleRules()

	s := &BackscatterSuppression{}
	pid := os.Getpid()
	// Multiple receivers in one process must own separate rules, so stopping
	// one receiver cannot remove another receiver's suppression.
	owner := fmt.Sprintf("spoof-tunnel-%d-%d", pid, suppressionID.Add(1))

	switch strings.ToLower(proto) {
	case "icmp":
		if port < 1 || port > 65535 {
			return s
		}
		// Match only this receiver's echo identifier, independent of TUN name.
		// Skipping the IPv4 header via IHL also supports IP options.
		rule := []string{"-p", "icmp", "--icmp-type", "echo-reply", "-m", "u32", "--u32", fmt.Sprintf("0>>22&0x3C@4>>16=0x%04x", port), "-m", "comment", "--comment", owner + "-icmp", "-j", "DROP"}
		s.addIptablesRule(rule)

	case "tcp":
		if port > 0 {
			rule := []string{"-p", "tcp", "--tcp-flags", "RST", "RST", "--sport", fmt.Sprintf("%d", port), "-m", "comment", "--comment", fmt.Sprintf("%s-rst-%d", owner, port), "-j", "DROP"}
			s.addIptablesRule(rule)
		}

	case "udp":
		if port > 0 {
			// First, try binding a dummy UDP listener on the port so the kernel stack
			// naturally knows a socket exists and never emits ICMP port unreachable.
			conn, err := net.ListenPacket("udp4", fmt.Sprintf("0.0.0.0:%d", port))
			if err == nil {
				s.dummyUDP = conn
			} else {
				// An existing listener already suppresses port-unreachable replies.
				// Never suppress unrelated ICMP errors across the entire host.
				log.Printf("[backscatter] UDP listener %d unavailable: %v", port, err)
			}
		}
	}
	return s
}

func (s *BackscatterSuppression) addIptablesRule(args []string) {
	checkArgs := append([]string{"-w", "5", "-C", "OUTPUT"}, args...)
	if err := exec.Command("iptables", checkArgs...).Run(); err == nil {
		return
	}
	insertArgs := append([]string{"-w", "5", "-I", "OUTPUT"}, args...)
	if err := exec.Command("iptables", insertArgs...).Run(); err == nil {
		s.mu.Lock()
		s.rules = append(s.rules, args)
		s.mu.Unlock()
	} else {
		log.Printf("[backscatter] note: unable to add iptables suppression rule (need root/CAP_NET_ADMIN): %v", err)
	}
}

// Stop closes the listener and removes only this receiver's iptables rules.
func (s *BackscatterSuppression) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.dummyUDP != nil {
		_ = s.dummyUDP.Close()
		s.dummyUDP = nil
	}

	for _, rule := range s.rules {
		delArgs := append([]string{"-w", "5", "-D", "OUTPUT"}, rule...)
		_ = exec.Command("iptables", delArgs...).Run()
	}
	s.rules = nil
}
