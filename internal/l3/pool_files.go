package l3

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strings"
)

func ParseSourceText(reader io.Reader) ([]net.IP, error) {
	s := bufio.NewScanner(reader)
	s.Buffer(make([]byte, 256), 4096)
	seen := map[string]bool{}
	out := []net.IP{}
	line := 0
	for s.Scan() {
		line++
		v := strings.TrimSpace(strings.TrimPrefix(s.Text(), "\ufeff"))
		if v == "" || strings.HasPrefix(v, "#") {
			continue
		}
		ip, ok := unicast4(v)
		if !ok {
			return nil, fmt.Errorf("line %d: expected one unicast IPv4 address", line)
		}
		key := ip.String()
		if !seen[key] {
			seen[key] = true
			out = append(out, ip)
			if len(out) > MaxPoolSources {
				return nil, fmt.Errorf("at most %d unique sources are supported", MaxPoolSources)
			}
		}
	}
	if err := s.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("source file is empty")
	}
	return out, nil
}
func EncodeSourceFile(ips []net.IP) []byte {
	out := make([]byte, len(ips)*4)
	for i, ip := range ips {
		copy(out[i*4:], ip.To4())
	}
	return out
}
func DecodeSourceFile(data []byte) ([]net.IP, error) {
	if len(data) == 0 || len(data)%4 != 0 || len(data)/4 > MaxPoolSources {
		return nil, fmt.Errorf("invalid stored pool")
	}
	out := make([]net.IP, 0, len(data)/4)
	for i := 0; i < len(data); i += 4 {
		n := binary.BigEndian.Uint32(data[i:])
		ip := net.IPv4(byte(n>>24), byte(n>>16), byte(n>>8), byte(n)).To4()
		if _, ok := unicast4(ip.String()); !ok {
			return nil, fmt.Errorf("invalid stored address")
		}
		out = append(out, ip)
	}
	return out, nil
}
