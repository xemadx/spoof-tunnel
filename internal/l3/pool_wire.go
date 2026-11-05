package l3

import (
	"bytes"
	"compress/zlib"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"sort"
)

const MaxPoolSources = 262144
const poolChunkBytes = 160

// All control messages are inside the existing AEAD frame, never plain ICMP.
type poolMessage struct {
	Ready   bool     `json:"q,omitempty"`
	Rate    int      `json:"r,omitempty"`
	Type    string   `json:"t"`
	Token   string   `json:"k,omitempty"`
	Source  string   `json:"s,omitempty"`
	Index   int      `json:"i,omitempty"`
	Total   int      `json:"n,omitempty"`
	Digest  string   `json:"h,omitempty"`
	Data    []byte   `json:"d,omitempty"`
	Missing []uint16 `json:"m,omitempty"`
}

func packSources(ips []net.IP) ([]byte, string, error) {
	if len(ips) == 0 || len(ips) > MaxPoolSources {
		return nil, "", fmt.Errorf("invalid source count")
	}
	vals := make([]uint32, 0, len(ips))
	seen := map[uint32]bool{}
	for _, ip := range ips {
		v := ip.To4()
		if v == nil {
			return nil, "", fmt.Errorf("invalid source")
		}
		n := binary.BigEndian.Uint32(v)
		if !seen[n] {
			seen[n] = true
			vals = append(vals, n)
		}
	}
	sort.Slice(vals, func(i, j int) bool { return vals[i] < vals[j] })
	raw := make([]byte, 4*len(vals))
	for i, v := range vals {
		binary.BigEndian.PutUint32(raw[i*4:], v)
	}
	sum := sha256.Sum256(raw)
	var buf bytes.Buffer
	z := zlib.NewWriter(&buf)
	if _, err := z.Write(raw); err != nil {
		return nil, "", err
	}
	if err := z.Close(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), hex.EncodeToString(sum[:]), nil
}
func unpackSources(data []byte, digest string) ([]net.IP, error) {
	if len(data) > 2<<20 {
		return nil, fmt.Errorf("compressed manifest too large")
	}
	z, err := zlib.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer z.Close()
	raw, err := io.ReadAll(io.LimitReader(z, 4*MaxPoolSources+1))
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 || len(raw) > 4*MaxPoolSources || len(raw)%4 != 0 {
		return nil, fmt.Errorf("invalid expanded manifest")
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != digest {
		return nil, fmt.Errorf("manifest digest mismatch")
	}
	out := make([]net.IP, 0, len(raw)/4)
	var last uint32
	for i := 0; i < len(raw); i += 4 {
		v := binary.BigEndian.Uint32(raw[i:])
		ip := net.IPv4(raw[i], raw[i+1], raw[i+2], raw[i+3]).To4()
		if _, ok := unicast4(ip.String()); !ok || i > 0 && v <= last {
			return nil, fmt.Errorf("invalid or duplicate manifest address")
		}
		last = v
		out = append(out, ip)
	}
	return out, nil
}

// This public demultiplexing hint keeps another link's busy raw socket from
// exhausting bootstrap admission. It grants no trust: AEAD remains mandatory.
func poolAdmissionPrefix(c Config, receive bool) []byte {
	key, err := base64.StdEncoding.DecodeString(c.PSK)
	if err != nil || len(key) != 32 {
		return nil
	}
	direction := "local-to-remote"
	if (c.Role == "remote") != receive {
		direction = "remote-to-local"
	}
	tag := derive(key, "spoof-pool-admission-v1/"+direction)
	out := append([]byte("EMC1"), tag[:8]...)
	return out
}
