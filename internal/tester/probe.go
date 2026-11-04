package tester

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net"
)

func newTestID() (string, error) {
	var b [16]byte
	_, err := rand.Read(b[:])
	return hex.EncodeToString(b[:]), err
}
func ValidTestID(id string) bool { b, e := hex.DecodeString(id); return e == nil && len(b) == 16 }
func probePayload(id string, src net.IP, sequence uint32, count int) []byte {
	b := make([]byte, 44)
	copy(b, "SPT2")
	binary.BigEndian.PutUint32(b[4:8], sequence)
	binary.BigEndian.PutUint32(b[8:12], uint32(count))
	key, _ := hex.DecodeString(id)
	mac := hmac.New(sha256.New, key)
	mac.Write(src.To4())
	mac.Write(b[:12])
	copy(b[12:], mac.Sum(nil))
	return b
}
func buildProbe(cfg TesterConfig, src, dst net.IP, sequence uint32) []byte {
	var packet []byte
	if cfg.Protocol == "icmp" {
		packet = BuildICMPEcho(src, dst, 0x5354, uint16(sequence))
	} else {
		packet = BuildTCPSyn(src, dst, cfg.DstPort)
	}
	if cfg.Protocol == "icmp" {
		packet = append(packet, probePayload(cfg.TestID, src, sequence, cfg.PacketCount)...)
	} else {
		// Ordinary 40-byte IPv4/TCP SYN, with no payload or options.
		// The sequence index uses ten source-port bits (max count is 1000).
		// The remaining port bits, IP ID and TCP sequence carry a 51-bit
		// truncated HMAC for diagnostic correlation, not tunnel encryption.
		tag := probePayload(cfg.TestID, src, sequence, cfg.PacketCount)[12:]
		// A nonzero IP ID is essential: Linux IP_HDRINCL fills a zero ID.
		binary.BigEndian.PutUint16(packet[4:6], binary.BigEndian.Uint16(tag[:2])|0x8000)
		copy(packet[24:28], tag[2:6])
		binary.BigEndian.PutUint16(packet[20:22], 49152+uint16(tag[6]&15)*1024+uint16(sequence))
	}
	binary.BigEndian.PutUint16(packet[2:4], uint16(len(packet)))
	packet[10] = 0
	packet[11] = 0
	binary.BigEndian.PutUint16(packet[10:12], internetChecksum(packet[:20]))
	if cfg.Protocol == "icmp" {
		packet[22] = 0
		packet[23] = 0
		binary.BigEndian.PutUint16(packet[22:24], internetChecksum(packet[20:]))
	} else {
		packet[36] = 0
		packet[37] = 0
		binary.BigEndian.PutUint16(packet[36:38], tcpChecksum(src.To4(), dst.To4(), packet[20:]))
	}
	return packet
}
func probeSequence(payload []byte, id string, src net.IP, packetCount int) (uint32, error) {
	if len(payload) != 44 || string(payload[:4]) != "SPT2" {
		return 0, fmt.Errorf("not a tester probe")
	}
	sequence := binary.BigEndian.Uint32(payload[4:8])
	if sequence >= uint32(packetCount) || binary.BigEndian.Uint32(payload[8:12]) != uint32(packetCount) {
		return 0, fmt.Errorf("out of range probe")
	}
	expected := probePayload(id, src, sequence, packetCount)
	if !hmac.Equal(payload[12:], expected[12:]) {
		return 0, fmt.Errorf("wrong test code or source")
	}
	return sequence, nil
}

// Recognize both the v0.3.7 payload format and the payload-free TCP SYN.
func tcpProbeSequence(packet []byte, ihl int, id string, count int) (uint32, error) {
	if ihl < 20 || len(packet) < ihl+20 {
		return 0, fmt.Errorf("short TCP probe")
	}
	header := int(packet[ihl+12]>>4) * 4
	if header < 20 || len(packet) < ihl+header {
		return 0, fmt.Errorf("bad TCP header")
	}
	if len(packet) > ihl+header {
		return probeSequence(packet[ihl+header:], id, net.IP(packet[12:16]), count)
	}
	port := binary.BigEndian.Uint16(packet[ihl : ihl+2])
	if port < 49152 || packet[ihl+13] != 2 {
		return 0, fmt.Errorf("not a tester SYN")
	}
	sequence := uint32(port-49152) % 1024
	if sequence >= uint32(count) {
		return 0, fmt.Errorf("out of range probe")
	}
	tag := probePayload(id, net.IP(packet[12:16]), sequence, count)[12:]
	got := append([]byte{}, packet[4:6]...)
	got = append(got, packet[ihl+4:ihl+8]...)
	got = append(got, byte((port-49152)/1024))
	want := append([]byte{}, tag[:6]...)
	want[0] |= 0x80
	want = append(want, tag[6]&15)
	if !hmac.Equal(got, want) {
		return 0, fmt.Errorf("wrong test code, source or parameters")
	}
	return sequence, nil
}
