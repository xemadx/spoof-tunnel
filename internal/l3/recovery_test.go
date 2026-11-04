package l3

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"testing"
	"time"
)

func TestFragmentationAndMTUFeedback(t *testing.T) {
	p := make([]byte, 2028)
	p[0] = 0x45
	p[8] = 64
	p[9] = 17
	copy(p[12:], []byte{10, 77, 0, 1, 10, 77, 0, 2})
	binary.BigEndian.PutUint16(p[2:], uint16(len(p)))
	for i := 20; i < len(p); i++ {
		p[i] = byte(i)
	}
	fragments, err := fitIPv4(p, 1280)
	if err != nil {
		t.Fatal(err)
	}
	var payload []byte
	for _, f := range fragments {
		if len(f) > 1280 || ipChecksum(f[:20]) != 0 {
			t.Fatal("bad fragment")
		}
		payload = append(payload, f[20:]...)
	}
	if len(fragments) != 2 || !bytes.Equal(payload, p[20:]) {
		t.Fatal("fragmentation lost data")
	}
	binary.BigEndian.PutUint16(p[6:], 0x4000)
	if _, err := fitIPv4(p, 1280); !errors.Is(err, errMTU) {
		t.Fatal(err)
	}
	reply := mtuReply(p, 1280)
	if reply == nil || reply[20] != 3 || reply[21] != 4 || binary.BigEndian.Uint16(reply[26:]) != 1280 || ipChecksum(reply[:20]) != 0 || ipChecksum(reply[20:]) != 0 {
		t.Fatal("invalid MTU reply")
	}
	p[9] = 1
	p[20] = 3
	if mtuReply(p, 1280) != nil {
		t.Fatal("error in response to ICMP error")
	}
}
func TestClockDiagnosticRequiresAuthentication(t *testing.T) {
	key := base64.StdEncoding.EncodeToString(make([]byte, 32))
	sender, _ := NewCodec(key, "local")
	receiver, _ := NewCodec(key, "remote")
	f := sender.Seal(2, []byte("challenge"))
	header := append([]byte(nil), f[:headerSize]...)
	binary.BigEndian.PutUint64(header[28:], uint64(time.Now().Add(60*time.Second).Unix()))
	var nonce [12]byte
	copy(nonce[4:], header[20:28])
	f = sender.txAEAD.Seal(header, nonce[:], append([]byte{2}, []byte("challenge")...), header)
	_, _, err := receiver.Open(f)
	var clock *ClockSkewError
	if !errors.As(err, &clock) {
		t.Fatalf("missing clock diagnostic: %v", err)
	}
	f[len(f)-1] ^= 1
	_, _, err = receiver.Open(f)
	if errors.As(err, &clock) {
		t.Fatal("unverified packet triggered clock warning")
	}
}

func TestFragmentOptionsCopySemantics(t *testing.T) {
	p := make([]byte, 2028)
	p[0] = 0x47
	p[8] = 64
	p[9] = 17
	binary.BigEndian.PutUint16(p[2:], uint16(len(p)))
	copy(p[20:28], []byte{0x82, 4, 1, 2, 0x02, 4, 3, 4})
	fragments, err := fitIPv4(p, 1280)
	if err != nil {
		t.Fatal(err)
	}
	if len(fragments) != 2 || fragments[0][0]&15 != 7 || fragments[1][0]&15 != 6 || !bytes.Equal(fragments[1][20:24], p[20:24]) {
		t.Fatal("incorrect copied IP options")
	}
}
