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
	packet = append(packet, probePayload(cfg.TestID, src, sequence, cfg.PacketCount)...)
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
