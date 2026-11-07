//go:build linux

package l3

import (
	"fmt"
	"log"
	"os"
	"os/exec"
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
}

// StartBackscatterSuppression configures suppression rules for the given
// carrier protocol ("icmp", "tcp", "udp") and port.
func StartBackscatterSuppression(proto string, port int) *BackscatterSuppression {
	s := &BackscatterSuppression{}
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
		// 2. Netfilter rule: drop outgoing echo-reply
		rule := []string{"-p", "icmp", "--icmp-type", "echo-reply", "-m", "comment", "--comment", "spoof-tunnel-icmp", "-j", "DROP"}
		s.addIptablesRule(rule)

	case "tcp":
		if port > 0 {
			rule := []string{"-p", "tcp", "--tcp-flags", "RST", "RST", "--sport", fmt.Sprintf("%d", port), "-m", "comment", "--comment", fmt.Sprintf("spoof-tunnel-rst-%d", port), "-j", "DROP"}
			s.addIptablesRule(rule)
		}

	case "udp":
		if port > 0 {
			rule := []string{"-p", "icmp", "--icmp-type", "port-unreachable", "-m", "comment", "--comment", fmt.Sprintf("spoof-tunnel-unreach-%d", port), "-j", "DROP"}
			s.addIptablesRule(rule)
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

	if s.sysctlRestore != "" {
		_ = os.WriteFile("/proc/sys/net/ipv4/icmp_echo_ignore_all", []byte(s.sysctlRestore+"\n"), 0644)
		s.sysctlRestore = ""
	}

	for _, rule := range s.rules {
		delArgs := append([]string{"-D", "OUTPUT"}, rule...)
		_ = exec.Command("iptables", delArgs...).Run()
	}
	s.rules = nil
}
