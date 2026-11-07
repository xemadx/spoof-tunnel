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
)

// BackscatterSuppression manages sysctl and iptables drop rules to prevent
// kernel backscatter (ICMP echo reply, TCP RST on closed ports, and ICMP
// port unreachable) for spoofed packets handled by raw sockets.
type BackscatterSuppression struct {
	mu            sync.Mutex
	sysctlRestore string
	rules         [][]string
	dummyUDP      net.PacketConn
}

// cleanupStaleRules scans the OUTPUT chain for rules added by spoof-tunnel
// processes that have died (e.g. killed via SIGKILL), cleaning them up.
func cleanupStaleRules() {
	out, err := exec.Command("iptables", "-S", "OUTPUT").Output()
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
						delArgs := append([]string{"-D", "OUTPUT"}, parts[2:]...)
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

	switch strings.ToLower(proto) {
	case "icmp":
		// 1. Sysctl: disable kernel ICMP echo responses
		if data, err := os.ReadFile("/proc/sys/net/ipv4/icmp_echo_ignore_all"); err == nil {
			val := strings.TrimSpace(string(data))
			if val != "1" {
				if err := os.WriteFile("/proc/sys/net/ipv4/icmp_echo_ignore_all", []byte("1\n"), 0644); err == nil {
					s.sysctlRestore = val
				}
			}
		}
		// 2. Netfilter rule: drop outgoing echo-reply tagged with this PID
		rule := []string{"-p", "icmp", "--icmp-type", "echo-reply", "-m", "comment", "--comment", fmt.Sprintf("spoof-tunnel-%d-icmp", pid), "-j", "DROP"}
		s.addIptablesRule(rule)

	case "tcp":
		if port > 0 {
			rule := []string{"-p", "tcp", "--tcp-flags", "RST", "RST", "--sport", fmt.Sprintf("%d", port), "-m", "comment", "--comment", fmt.Sprintf("spoof-tunnel-%d-rst-%d", pid, port), "-j", "DROP"}
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
				// Fallback to iptables drop rule if port cannot be bound
				rule := []string{"-p", "icmp", "--icmp-type", "port-unreachable", "-m", "comment", "--comment", fmt.Sprintf("spoof-tunnel-%d-unreach-%d", pid, port), "-j", "DROP"}
				s.addIptablesRule(rule)
			}
		}
	}
	return s
}

func (s *BackscatterSuppression) addIptablesRule(args []string) {
	checkArgs := append([]string{"-C", "OUTPUT"}, args...)
	if err := exec.Command("iptables", checkArgs...).Run(); err == nil {
		return
	}
	insertArgs := append([]string{"-I", "OUTPUT"}, args...)
	if err := exec.Command("iptables", insertArgs...).Run(); err == nil {
		s.mu.Lock()
		s.rules = append(s.rules, args)
		s.mu.Unlock()
	} else {
		log.Printf("[backscatter] note: unable to add iptables suppression rule (need root/CAP_NET_ADMIN): %v", err)
	}
}

// Stop restores previous sysctl settings and removes added iptables rules.
func (s *BackscatterSuppression) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.dummyUDP != nil {
		_ = s.dummyUDP.Close()
		s.dummyUDP = nil
	}

	for _, rule := range s.rules {
		delArgs := append([]string{"-D", "OUTPUT"}, rule...)
		_ = exec.Command("iptables", delArgs...).Run()
	}
	s.rules = nil

	if s.sysctlRestore != "" {
		// Only restore sysctl if no other spoof-tunnel rules remain active in OUTPUT
		out, err := exec.Command("iptables", "-S", "OUTPUT").Output()
		if err != nil || !strings.Contains(string(out), "spoof-tunnel-") {
			_ = os.WriteFile("/proc/sys/net/ipv4/icmp_echo_ignore_all", []byte(s.sysctlRestore+"\n"), 0644)
			s.sysctlRestore = ""
		}
	}
}
