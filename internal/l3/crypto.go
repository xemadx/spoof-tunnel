package l3

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"sync"
	"time"
)

const headerSize = 36

type ClockSkewError struct{ Stamp, Now int64 }

func (e *ClockSkewError) Error() string {
	return "authenticated frame timestamp outside acceptance window; check clock synchronization or delayed traffic"
}

var errFrame = errors.New("invalid, expired, unauthenticated or replayed frame")

type replay struct {
	max   uint64
	seen  [1024]uint64
	until int64
	aead  cipher.AEAD
}
type Codec struct {
	mu      sync.RWMutex
	tx, rx  []byte
	txAEAD  cipher.AEAD
	session [16]byte
	seq     uint64
	peers   map[[16]byte]*replay
}

func derive(key []byte, label string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(label))
	return h.Sum(nil)
}

// sessionAEAD derives the per-session key once and returns a reusable AEAD.
// Derivation is a HMAC plus an AES key schedule; doing it for every packet
// dominated the receive path at high packet rates.
func sessionAEAD(key, session []byte) cipher.AEAD {
	b, _ := aes.NewCipher(derive(key, string(session)))
	a, _ := cipher.NewGCM(b)
	return a
}

func NewCodec(psk, role string) (*Codec, error) {
	key, err := base64.StdEncoding.DecodeString(psk)
	if err != nil || len(key) != 32 {
		return nil, errFrame
	}
	a, b := "spoof-l3-v1/local-to-remote", "spoof-l3-v1/remote-to-local"
	if role == "remote" {
		a, b = b, a
	}
	c := &Codec{tx: derive(key, a), rx: derive(key, b), peers: make(map[[16]byte]*replay)}
	if _, err = rand.Read(c.session[:]); err != nil {
		return nil, err
	}
	c.txAEAD = sessionAEAD(c.tx, c.session[:])
	return c, nil
}

func (c *Codec) Seal(kind byte, payload []byte) []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seq++
	var h [headerSize]byte
	copy(h[:], "EML1")
	copy(h[4:20], c.session[:])
	binary.BigEndian.PutUint64(h[20:28], c.seq)
	binary.BigEndian.PutUint64(h[28:36], uint64(time.Now().Unix()))
	var nonce [12]byte
	copy(nonce[4:], h[20:28])
	plain := make([]byte, 1+len(payload))
	plain[0] = kind
	copy(plain[1:], payload)
	return c.txAEAD.Seal(h[:], nonce[:], plain, h[:])
}

func (c *Codec) Open(frame []byte) (byte, []byte, error) {
	if len(frame) < headerSize+17 || string(frame[:4]) != "EML1" {
		return 0, nil, errFrame
	}
	now := time.Now().Unix()
	stamp := int64(binary.BigEndian.Uint64(frame[28:36]))
	var session [16]byte
	copy(session[:], frame[4:20])
	seq := binary.BigEndian.Uint64(frame[20:28])
	if seq == 0 {
		return 0, nil, errFrame
	}
	var nonce [12]byte
	copy(nonce[4:], frame[20:28])

	// Reuse the AEAD of an already admitted session; derive only for a new one.
	c.mu.RLock()
	var a cipher.AEAD
	if r := c.peers[session]; r != nil {
		a = r.aead
	}
	c.mu.RUnlock()
	if a == nil {
		a = sessionAEAD(c.rx, session[:])
	}

	plain, err := a.Open(nil, nonce[:], frame[headerSize:], frame[:headerSize])
	if err != nil || len(plain) == 0 {
		return 0, nil, errFrame
	}

	if stamp < now-120 || stamp > now+30 {
		return 0, nil, &ClockSkewError{Stamp: stamp, Now: now}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	r := c.peers[session]
	if r == nil {
		for id, p := range c.peers {
			if p.until < now {
				delete(c.peers, id)
			}
		}
		if len(c.peers) >= 64 {
			return 0, nil, errFrame
		}
		r = &replay{aead: a}
		c.peers[session] = r
	} else if r.aead == nil {
		r.aead = a
	}
	if seq <= r.max && r.max-seq >= 1024 {
		return 0, nil, errFrame
	}
	if r.seen[seq%1024] == seq {
		return 0, nil, errFrame
	}
	r.seen[seq%1024] = seq
	if seq > r.max {
		r.max = seq
	}
	if stamp+121 > r.until {
		r.until = stamp + 121
	}
	return plain[0], plain[1:], nil
}
