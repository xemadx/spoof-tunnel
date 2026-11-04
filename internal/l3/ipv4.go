package l3

import (
	"encoding/binary"
	"errors"
	"net"
)

var errMTU = errors.New("IPv4 packet exceeds MTU with DF set")

func validIPv4(p []byte) bool {
	return len(p) >= 20 && p[0]>>4 == 4 && int(p[0]&15)*4 >= 20 && int(p[0]&15)*4 <= len(p) && int(binary.BigEndian.Uint16(p[2:4])) == len(p)
}
func ipChecksum(p []byte) uint16 {
	var sum uint32
	for i := 0; i+1 < len(p); i += 2 {
		sum += uint32(binary.BigEndian.Uint16(p[i:]))
	}
	if len(p)%2 != 0 {
		sum += uint32(p[len(p)-1]) << 8
	}
	for sum>>16 != 0 {
		sum = (sum & 65535) + (sum >> 16)
	}
	return ^uint16(sum)
}
func fitIPv4(p []byte, mtu int) ([][]byte, error) {
	if !validIPv4(p) {
		return nil, errors.New("invalid inner IPv4 packet")
	}
	if len(p) <= mtu {
		return [][]byte{p}, nil
	}
	flags := binary.BigEndian.Uint16(p[6:8])
	if flags&0x4000 != 0 {
		return nil, errMTU
	}
	ihl := int(p[0]&15) * 4
	var copied []byte
	for i := 20; i < ihl; {
		kind := p[i]
		if kind == 0 {
			break
		}
		if kind == 1 {
			i++
			continue
		}
		if i+1 >= ihl || int(p[i+1]) < 2 || i+int(p[i+1]) > ihl {
			return nil, errors.New("invalid IPv4 options")
		}
		n := int(p[i+1])
		if kind&128 != 0 {
			copied = append(copied, p[i:i+n]...)
		}
		i += n
	}
	for len(copied)%4 != 0 {
		copied = append(copied, 0)
	}
	var out [][]byte
	for offset := 0; offset < len(p)-ihl; {
		header := append([]byte(nil), p[:ihl]...)
		if offset > 0 {
			header = append(append([]byte(nil), p[:20]...), copied...)
		}
		size := mtu - len(header)
		remaining := len(p) - ihl - offset
		if remaining > size {
			size = size / 8 * 8
		}
		if size <= 0 {
			return nil, errMTU
		}
		if size > remaining {
			size = remaining
		}
		frag := append(header, p[ihl+offset:ihl+offset+size]...)
		frag[0] = 0x40 | byte(len(header)/4)
		fragmentOffset := int(flags&0x1fff) + offset/8
		if fragmentOffset > 0x1fff {
			return nil, errors.New("invalid fragment offset")
		}
		field := uint16(fragmentOffset)
		if offset+size < len(p)-ihl || flags&0x2000 != 0 {
			field |= 0x2000
		}
		binary.BigEndian.PutUint16(frag[6:8], field)
		binary.BigEndian.PutUint16(frag[2:4], uint16(len(frag)))
		frag[10] = 0
		frag[11] = 0
		binary.BigEndian.PutUint16(frag[10:12], ipChecksum(frag[:len(header)]))
		out = append(out, frag)
		offset += size
	}
	return out, nil
}
func mtuReply(p []byte, mtu int, router ...net.IP) []byte {
	if !validIPv4(p) || binary.BigEndian.Uint16(p[6:8])&0x1fff != 0 {
		return nil
	}
	src, dst := net.IP(p[12:16]), net.IP(p[16:20])
	if src.IsUnspecified() || src.IsMulticast() || src.Equal(net.IPv4bcast) || dst.IsMulticast() || dst.Equal(net.IPv4bcast) {
		return nil
	}
	ihl := int(p[0]&15) * 4
	if p[9] == 1 && len(p) > ihl {
		switch p[ihl] {
		case 3, 4, 5, 11, 12:
			return nil
		}
	}
	quote := ihl + 8
	if quote > len(p) {
		quote = len(p)
	}
	out := make([]byte, 28+quote)
	out[0] = 0x45
	out[8] = 64
	out[9] = 1
	copy(out[12:16], dst)
	if len(router) > 0 && router[0].To4() != nil {
		copy(out[12:16], router[0].To4())
	}
	copy(out[16:20], src)
	binary.BigEndian.PutUint16(out[2:4], uint16(len(out)))
	out[20] = 3
	out[21] = 4
	binary.BigEndian.PutUint16(out[26:28], uint16(mtu))
	copy(out[28:], p[:quote])
	binary.BigEndian.PutUint16(out[22:24], ipChecksum(out[20:]))
	binary.BigEndian.PutUint16(out[10:12], ipChecksum(out[:20]))
	return out
}
