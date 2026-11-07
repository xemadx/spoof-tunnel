//go:build linux

package tester

import (
	"encoding/binary"
	"fmt"
	"log"
	"net"
	"syscall"
	"time"

	"github.com/ParsaKSH/spoof-tunnel/internal/l3"
)

type tokenPacer struct {
	rate       float64
	capacity   float64
	tokens     float64
	lastRefill time.Time
}

func newTokenPacer(rate, capacity float64) *tokenPacer {
	return &tokenPacer{
		rate:       rate,
		capacity:   capacity,
		tokens:     capacity,
		lastRefill: time.Now(),
	}
}

func (p *tokenPacer) wait(cancel <-chan struct{}) bool {
	for {
		select {
		case <-cancel:
			return false
		default:
		}
		now := time.Now()
		elapsed := now.Sub(p.lastRefill).Seconds()
		p.tokens += elapsed * p.rate
		if p.tokens > p.capacity {
			p.tokens = p.capacity
		}
		p.lastRefill = now

		if p.tokens >= 1.0 {
			p.tokens -= 1.0
			return true
		}
		needed := (1.0 - p.tokens) / p.rate
		sleepDur := time.Duration(needed * float64(time.Second))
		if sleepDur < time.Millisecond {
			sleepDur = time.Millisecond
		}
		select {
		case <-cancel:
			return false
		case <-time.After(sleepDur):
		}
	}
}

func (r *Runner) doSend(cfg TesterConfig, ranges *IPRangeSet, cancel <-chan struct{}) error {
	dstIP := net.ParseIP(cfg.DstIP).To4()
	if dstIP == nil {
		return fmt.Errorf("invalid dst_ip: %s", cfg.DstIP)
	}

	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_RAW, syscall.IPPROTO_RAW)
	if err != nil {
		return fmt.Errorf("raw socket: %w (need root/CAP_NET_RAW)", err)
	}
	defer syscall.Close(fd)

	if err := syscall.SetsockoptInt(fd, syscall.IPPROTO_IP, syscall.IP_HDRINCL, 1); err != nil {
		return fmt.Errorf("setsockopt IP_HDRINCL: %w", err)
	}

	addr := syscall.SockaddrInet4{}
	copy(addr.Addr[:], dstIP)

	total := ranges.Total()
	packetCount := cfg.PacketCount
	if packetCount < 1 {
		packetCount = 10
	}

	log.Printf("[tester-sender] protocol=%s dst=%s sources=%d packets_per_ip=%d",
		cfg.Protocol, dstIP, total, packetCount)

	pacer := newTokenPacer(50000, 10000)
	var sent, errCount int64
	var idx int
	defer func() {
		r.mu.Lock()
		r.state.ProcessedIPs = int64(idx)
		r.state.SentPackets = sent
		r.state.FailedPackets = errCount
		r.mu.Unlock()
	}()
	ranges.IterateIPs(func(srcIP net.IP) bool {
		// Check cancel
		select {
		case <-cancel:
			return false
		default:
		}

		for p := 0; p < packetCount; p++ {
			if !pacer.wait(cancel) {
				return false
			}
			pkt := buildProbe(cfg, srcIP, dstIP, uint32(p))

			if err := syscall.Sendto(fd, pkt, 0, &addr); err != nil {
				errCount++
				continue
			}
			sent++
		}

		idx++
		r.mu.Lock()
		r.state.Progress = int(int64(idx) * 100 / total)
		r.state.ProcessedIPs = int64(idx)
		r.state.SentPackets = sent
		r.state.FailedPackets = errCount
		r.mu.Unlock()
		return true
	})

	log.Printf("[tester-sender] done -- sent: %d, errors: %d", sent, errCount)
	if int64(idx) != total {
		return fmt.Errorf("sender stopped after %d of %d source IPs", idx, total)
	}
	if errCount > 0 {
		return fmt.Errorf("%d packets failed to send (%d sent)", errCount, sent)
	}
	return nil
}

func (r *Runner) doReceive(cfg TesterConfig, ranges *IPRangeSet, cancel <-chan struct{}, ready chan<- error) (err error) {
	announced := false
	defer func() {
		if !announced {
			ready <- err
		}
	}()
	port := cfg.DstPort
	if cfg.Protocol == "icmp" {
		port = 0
	}
	suppression := l3.StartBackscatterSuppression(cfg.Protocol, port)
	defer suppression.Stop()

	var proto int
	switch cfg.Protocol {
	case "tcp":
		proto = syscall.IPPROTO_TCP
	case "icmp":
		proto = syscall.IPPROTO_ICMP
	default:
		return fmt.Errorf("unsupported protocol: %s", cfg.Protocol)
	}

	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_RAW, proto)
	if err != nil {
		return fmt.Errorf("raw socket: %w (need root/CAP_NET_RAW)", err)
	}
	defer syscall.Close(fd)

	tv := syscall.Timeval{Sec: 1}
	if err := syscall.SetsockoptTimeval(fd, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &tv); err != nil {
		return fmt.Errorf("setsockopt SO_RCVTIMEO: %w", err)
	}

	// Best effort buffering for bursty diagnostic traffic. Start acknowledges
	// readiness only after the raw socket and receive timeout are configured.
	_ = syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_RCVBUF, 4*1024*1024)
	announced = true
	ready <- nil

	timeout := cfg.Timeout
	if timeout < 1 {
		timeout = 30
	}
	packetCount := cfg.PacketCount
	if packetCount < 1 {
		packetCount = 10
	}
	maxLoss := cfg.MaxPacketLoss

	log.Printf("[tester-receiver] protocol=%s sources=%d packet_count=%d max_loss=%.1f%% timeout=%ds",
		cfg.Protocol, ranges.Total(), packetCount, maxLoss, timeout)

	seen := make(map[uint32][]uint64)
	buf := make([]byte, 65535)
	deadline := time.Now().Add(time.Duration(timeout) * time.Second)
	startTime := time.Now()

	// Periodic flush ticker — every 5 minutes
	flushInterval := 5 * time.Minute
	lastFlush := time.Now()

	for time.Now().Before(deadline) {
		select {
		case <-cancel:
			goto buildResults
		default:
		}

		elapsed := time.Since(startTime)
		total := time.Duration(timeout) * time.Second
		r.mu.Lock()
		r.state.Progress = int(elapsed * 100 / total)
		r.mu.Unlock()

		// Periodic flush to disk
		if time.Since(lastFlush) >= flushInterval {
			r.flushInterimResults(packetCount, maxLoss)
			lastFlush = time.Now()
		}

		n, _, err := syscall.Recvfrom(fd, buf, 0)
		if err != nil {
			continue
		}
		if n < 20 {
			continue
		}

		ihl := int(buf[0]&0x0f) * 4
		if buf[0]>>4 != 4 || ihl < 20 || n < ihl {
			continue
		}
		srcIPu32 := binary.BigEndian.Uint32(buf[12:16])

		if !ranges.Contains(srcIPu32) {
			continue
		}

		if proto == syscall.IPPROTO_ICMP && (n < ihl+8 || buf[ihl] != 8 || buf[ihl+1] != 0) {
			continue
		}
		if proto == syscall.IPPROTO_TCP && (n < ihl+20 || buf[ihl+13]&0x02 == 0) {
			continue
		}
		// Filter by port if specified and protocol supports it (e.g. TCP)
		if cfg.DstPort > 0 && proto == syscall.IPPROTO_TCP {
			if n < ihl+4 {
				continue
			}
			dstPort := binary.BigEndian.Uint16(buf[ihl+2 : ihl+4])
			if int(dstPort) != cfg.DstPort {
				continue
			}
		}

		length := int(binary.BigEndian.Uint16(buf[2:4]))
		valid := length >= ihl && length <= n && internetChecksum(buf[:ihl]) == 0
		if valid {
			n = length
			if proto == syscall.IPPROTO_ICMP {
				valid = n >= ihl+8 && internetChecksum(buf[ihl:n]) == 0
			} else {
				valid = n >= ihl+20 && tcpChecksum(net.IP(buf[12:16]), net.IP(buf[16:20]), buf[ihl:n]) == 0
			}
		}
		if !valid {
			r.recvMu.Lock()
			r.recvRejected++
			r.recvMu.Unlock()
			continue
		}
		// Count rejected candidate echo/SYN packets from the configured source
		// pool and destination port; this includes unrelated traffic, so a
		// rejection alone is not proof of an incorrect test code.
		var sequence uint32
		var probeErr error
		if proto == syscall.IPPROTO_TCP {
			sequence, probeErr = tcpProbeSequence(buf[:n], ihl, cfg.TestID, packetCount)
		} else {
			sequence, probeErr = probeSequence(buf[ihl+8:n], cfg.TestID, net.IP(buf[12:16]), packetCount)
		}
		if probeErr != nil {
			r.recvMu.Lock()
			r.recvRejected++
			r.recvMu.Unlock()
			continue
		}
		bits := seen[srcIPu32]
		if bits == nil {
			bits = make([]uint64, (packetCount+63)/64)
			seen[srcIPu32] = bits
		}
		mask := uint64(1) << (sequence % 64)
		if bits[sequence/64]&mask != 0 {
			r.recvMu.Lock()
			r.recvDuplicates++
			r.recvMu.Unlock()
			continue
		}
		bits[sequence/64] |= mask
		r.recvMu.Lock()
		before, exists := r.received[srcIPu32]
		if !exists {
			r.receivedOrder = append(r.receivedOrder, srcIPu32)
		}
		after := before + 1
		if after > packetCount {
			after = packetCount
		}
		r.received[srcIPu32] = after
		beforePassed := exists && float64(packetCount-before)/float64(packetCount)*100 <= maxLoss
		afterPassed := float64(packetCount-after)/float64(packetCount)*100 <= maxLoss
		if !beforePassed && afterPassed {
			r.recvPassed++
		}
		r.recvMu.Unlock()
	}

buildResults:
	// Final flush — write definitive results to disk
	r.flushInterimResults(packetCount, maxLoss)

	r.recvMu.RLock()
	recvTotal := len(r.received)
	r.recvMu.RUnlock()

	r.mu.Lock()
	passedCount := r.state.PassedCount
	r.mu.Unlock()

	log.Printf("[tester-receiver] done -- %d/%d IPs received packets, %d passed (loss <= %.1f%%)",
		recvTotal, ranges.Total(), passedCount, maxLoss)

	// Clear the live received map to free memory
	r.recvMu.Lock()
	r.received = nil
	r.receivedOrder = nil
	r.recvMu.Unlock()

	return nil
}
