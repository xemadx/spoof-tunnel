// Package benchmark compares ordered, authenticated carrier pairs. It never
// creates a TUN, modifies a route, or stops an existing tunnel during testing.
package benchmark

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"sort"
	"sync"
	"time"

	"github.com/ParsaKSH/spoof-tunnel/internal/l3"
)

var Carriers = []string{"icmp", "udp", "tcp", "ipip", "experimental"}

const payloadBytes = 1100
const controlKind byte = 210
const dataKind byte = 211

type Wire interface {
	Send([]byte) error
	Receive() ([]byte, error)
	Close()
}
type Factory func(l3.Config, []byte) (Wire, error)
type Hook func(context.Context, string, json.RawMessage) (any, error)

// Invitation describes the controller (A) and receiver (B), including one fixed
// spoof source per direction. Key is a short-lived, narrowly scoped capability.
type Invitation struct {
	Version int    `json:"v"`
	Key     string `json:"key"`
	A       string `json:"a"`
	B       string `json:"b"`
	SourceA string `json:"source_a"`
	SourceB string `json:"source_b"`
	PortA   int    `json:"port_a"`
	PortB   int    `json:"port_b"`
	Expires int64  `json:"expires"`
	Target  uint   `json:"target"`
}
type Options struct {
	RateMbps int `json:"rate_mbps"`
	Seconds  int `json:"seconds"`
}

func (o Options) Validate() error {
	if o.RateMbps < 1 || o.RateMbps > 60 || o.Seconds < 2 || o.Seconds > 8 {
		return errors.New("benchmark rate must be 1–60 Mbps and duration 2–8 seconds per direction")
	}
	return nil
}
func (o Options) count() int { return o.RateMbps * 1000000 * o.Seconds / (8 * payloadBytes) }

type Direction struct {
	Sent       int     `json:"sent"`
	Received   int     `json:"received"`
	SendErrors int     `json:"send_errors"`
	Seconds    float64 `json:"seconds"`
	Mbps       float64 `json:"mbps"`
	LossPct    float64 `json:"loss_pct"`
	Capped     bool    `json:"capped"`
}
type Result struct {
	Index       int       `json:"index"`
	Send        string    `json:"send"`
	Receive     string    `json:"receive"`
	Status      string    `json:"status"`
	Error       string    `json:"error,omitempty"`
	RTTMs       float64   `json:"rtt_ms"`
	P95Ms       float64   `json:"p95_ms"`
	PingSent    int       `json:"ping_sent"`
	PingReplies int       `json:"ping_replies"`
	Upload      Direction `json:"upload"`
	Download    Direction `json:"download"`
	Score       float64   `json:"score"`
}
type Snapshot struct {
	Endpoints   Endpoints `json:"endpoints"`
	Role        string    `json:"role"`
	Status      string    `json:"status"`
	Phase       string    `json:"phase"`
	Current     int       `json:"current"`
	Progress    int       `json:"progress"`
	Expires     int64     `json:"expires"`
	Options     Options   `json:"options"`
	Results     []Result  `json:"results"`
	Recommended int       `json:"recommended"`
	Error       string    `json:"error,omitempty"`
	Applied     bool      `json:"applied"`
}
type Endpoints struct {
	ControllerIP     string `json:"controller_ip"`
	PeerIP           string `json:"peer_ip"`
	ControllerSource string `json:"controller_source"`
	PeerSource       string `json:"peer_source"`
	ControllerPort   int    `json:"controller_port"`
	PeerPort         int    `json:"peer_port"`
}
type message struct {
	ID     string          `json:"id"`
	Reply  string          `json:"reply,omitempty"`
	Op     string          `json:"op"`
	Trial  int             `json:"trial"`
	Return string          `json:"return"`
	Body   json.RawMessage `json:"body,omitempty"`
	Error  string          `json:"error,omitempty"`
}
type received struct {
	msg     message
	carrier string
}
type counter struct {
	bits  []uint64
	count int
}

func (c *counter) add(seq int) {
	if seq < 0 || seq/64 >= len(c.bits) {
		return
	}
	mask := uint64(1) << uint(seq%64)
	if c.bits[seq/64]&mask == 0 {
		c.bits[seq/64] |= mask
		c.count++
	}
}

type trial struct {
	index                         int
	options                       Options
	send, receive                 string
	up, down                      counter
	downloadStarted, downloadDone bool
	download                      Direction
}
type Session struct {
	mu       sync.Mutex
	rpcMu    sync.Mutex
	ctx      context.Context
	cancel   context.CancelFunc
	wires    map[string]Wire
	tx, bulk map[string]*l3.Codec
	rx       *l3.Codec
	inv      Invitation
	view     Snapshot
	current  *trial
	pending  map[string]chan message
	cache    map[string]message
	inbox    chan received
	hook     Hook
	closed   sync.Once
	wg       sync.WaitGroup
}
type Runner struct {
	mu      sync.Mutex
	session *Session
	factory Factory
}

func NewRunner(factory Factory) *Runner { return &Runner{factory: factory} }
func validIP(s string) bool {
	ip := net.ParseIP(s)
	return ip != nil && ip.To4() != nil && !ip.IsUnspecified() && !ip.IsMulticast() && !ip.Equal(net.IPv4bcast)
}
func (i Invitation) Validate() error {
	key, err := base64.StdEncoding.DecodeString(i.Key)
	if err != nil || len(key) != 32 || i.Version != 1 {
		return errors.New("invalid benchmark code")
	}
	for _, ip := range []string{i.A, i.B, i.SourceA, i.SourceB} {
		if !validIP(ip) {
			return errors.New("benchmark endpoints and sources must be unicast IPv4")
		}
	}
	if i.A == i.B || i.SourceA == i.SourceB || i.PortA < 1 || i.PortA > 65535 || i.PortB < 1 || i.PortB > 65535 {
		return errors.New("benchmark endpoints, sources or ports conflict")
	}
	if i.Expires <= time.Now().Unix() || i.Expires > time.Now().Add(21*time.Minute).Unix() {
		return errors.New("benchmark code expired; generate a fresh code on the other server")
	}
	return nil
}
func Decode(code string) (Invitation, error) {
	var i Invitation
	b, e := base64.RawURLEncoding.DecodeString(code)
	if e != nil || len(b) > 2048 {
		return i, errors.New("invalid benchmark code")
	}
	if e = json.Unmarshal(b, &i); e != nil {
		return i, errors.New("invalid benchmark code")
	}
	return i, i.Validate()
}
func (r *Runner) Receiver(i Invitation, hook Hook) (string, error) {
	key := make([]byte, 32)
	if _, e := rand.Read(key); e != nil {
		return "", e
	}
	i.Key = base64.StdEncoding.EncodeToString(key)
	i.Version = 1
	i.Expires = time.Now().Add(20 * time.Minute).Unix()
	if e := i.Validate(); e != nil {
		return "", e
	}
	if _, e := r.start(i, "remote", Options{}, hook); e != nil {
		return "", e
	}
	b, _ := json.Marshal(i)
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func (r *Runner) Controller(code string, o Options, hook Hook) error {
	if e := o.Validate(); e != nil {
		return e
	}
	i, e := Decode(code)
	if e != nil {
		return e
	}
	s, e := r.start(i, "local", o, hook)
	if e != nil {
		return e
	}
	s.wg.Add(1)
	go func() { defer s.wg.Done(); s.run(o) }()
	return nil
}
func (r *Runner) start(i Invitation, role string, o Options, hook Hook) (*Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.session != nil {
		v := r.session.Status()
		if v.Status == "running" || v.Status == "waiting" || v.Status == "applying" || v.Status == "done" && !v.Applied {
			return nil, errors.New("stop the previous benchmark session before starting another")
		}
		r.session.Close()
	}
	ctx, cancel := context.WithDeadline(context.Background(), time.Unix(i.Expires, 0))
	s := &Session{ctx: ctx, cancel: cancel, inv: i, hook: hook, wires: map[string]Wire{}, tx: map[string]*l3.Codec{}, bulk: map[string]*l3.Codec{}, pending: map[string]chan message{}, cache: map[string]message{}, inbox: make(chan received, 128), view: Snapshot{Role: role, Status: "waiting", Current: -1, Recommended: -1, Expires: i.Expires, Options: o, Results: []Result{}}}
	s.rx, _ = l3.NewCodec(i.Key, role)
	s.view.Endpoints = Endpoints{i.A, i.B, i.SourceA, i.SourceB, i.PortA, i.PortB}
	sum := sha256.Sum256([]byte(i.Key))
	tag := append([]byte("SBF1"), sum[:8]...)
	for _, carrier := range Carriers {
		c := l3.Config{LocalIP: "0.0.0.0", PeerIP: i.B, SpoofSource: i.SourceA, PeerSpoof: i.SourceB, Send: carrier, Receive: carrier, SendPort: i.PortB, ReceivePort: i.PortA}
		if role == "remote" {
			c.PeerIP = i.A
			c.SpoofSource = i.SourceB
			c.PeerSpoof = i.SourceA
			c.SendPort = i.PortA
			c.ReceivePort = i.PortB
		}
		w, e := r.factory(c, tag)
		if e != nil {
			s.Close()
			return nil, fmt.Errorf("open %s: %w", carrier, e)
		}
		s.wires[carrier] = w
		s.tx[carrier], _ = l3.NewCodec(i.Key, role)
		s.bulk[carrier], _ = l3.NewCodec(i.Key, role)
	}
	r.session = s
	for _, carrier := range Carriers {
		s.wg.Add(1)
		go func(name string) { defer s.wg.Done(); s.read(name) }(carrier)
	}
	s.wg.Add(1)
	go func() { defer s.wg.Done(); s.serve() }()
	go func() {
		<-ctx.Done()
		s.Close()
		s.mu.Lock()
		if s.view.Status == "waiting" || s.view.Status == "running" || s.view.Status == "applying" || (s.view.Status == "done" && !s.view.Applied) {
			s.view.Status = "stopped"
			s.view.Phase = ""
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				s.view.Error = "نشست آزمون منقضی شد؛ برای آزمون یا اعمال نتیجه، کد تازه بساز."
			}
		}
		s.mu.Unlock()
	}()
	return s, nil
}
func (r *Runner) Status() Snapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.session == nil {
		return Snapshot{Status: "idle", Recommended: -1, Current: -1, Results: []Result{}}
	}
	return r.session.Status()
}
func (r *Runner) Stop() {
	r.mu.Lock()
	s := r.session
	r.mu.Unlock()
	if s != nil {
		s.Close()
		s.wg.Wait()
		s.mu.Lock()
		s.view.Status = "stopped"
		s.view.Phase = ""
		s.mu.Unlock()
	}
}
func (r *Runner) Session() *Session       { r.mu.Lock(); defer r.mu.Unlock(); return r.session }
func (s *Session) Invitation() Invitation { return s.inv }
func (s *Session) Status() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.view
	v.Results = append([]Result{}, v.Results...)
	return v
}
func (s *Session) Close() {
	s.closed.Do(func() {
		s.cancel()
		for _, w := range s.wires {
			w.Close()
		}
	})
}
func (s *Session) send(carrier string, m message) error {
	b, _ := json.Marshal(m)
	if len(b) > 1100 {
		return errors.New("benchmark control message exceeds the bounded datagram size")
	}
	w := s.wires[carrier]
	if w == nil {
		return errors.New("invalid carrier")
	}
	return w.Send(s.tx[carrier].Seal(controlKind, b))
}
func randomID() string { var b [16]byte; _, _ = rand.Read(b[:]); return hex.EncodeToString(b[:]) }
func (s *Session) rpc(ctx context.Context, send, receive, op string, index int, body any, attempts int) (json.RawMessage, error) {
	b, _ := json.Marshal(body)
	m := message{ID: randomID(), Op: op, Trial: index, Return: receive, Body: b}
	ch := make(chan message, 1)
	s.mu.Lock()
	s.pending[m.ID] = ch
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.pending, m.ID); s.mu.Unlock() }()
	for n := 0; n < attempts; n++ {
		if e := s.send(send, m); e != nil {
			return nil, e
		}
		timer := time.NewTimer(800 * time.Millisecond)
		select {
		case reply := <-ch:
			timer.Stop()
			if reply.Error != "" {
				return nil, errors.New(reply.Error)
			}
			return reply.Body, nil
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-s.ctx.Done():
			timer.Stop()
			return nil, s.ctx.Err()
		}
	}
	return nil, errors.New("no authenticated response on this carrier pair")
}
func (s *Session) read(carrier string) {
	for {
		frame, e := s.wires[carrier].Receive()
		if e != nil {
			return
		}
		kind, b, e := s.rx.Open(frame)
		if e != nil {
			continue
		}
		switch kind {
		case controlKind:
			if len(b) > 32768 {
				continue
			}
			var m message
			if json.Unmarshal(b, &m) != nil {
				continue
			}
			if m.Reply != "" {
				s.mu.Lock()
				ch := s.pending[m.Reply]
				s.mu.Unlock()
				if ch != nil {
					select {
					case ch <- m:
					default:
					}
				}
				continue
			}
			if s.view.Role != "remote" {
				continue
			}
			select {
			case s.inbox <- received{m, carrier}:
			default:
			}
		case dataKind:
			if len(b) != payloadBytes || b[3] != 0 {
				continue
			}
			index, dir, seq := int(binary.BigEndian.Uint16(b)), b[2], int(binary.BigEndian.Uint32(b[4:]))
			s.mu.Lock()
			t := s.current
			if t != nil && t.index == index && seq < t.options.count() {
				if dir == 0 && s.view.Role == "remote" && carrier == t.send {
					t.up.add(seq)
				}
				if dir == 1 && s.view.Role == "local" && carrier == t.receive {
					t.down.add(seq)
				}
			}
			s.mu.Unlock()
		}
	}
}
func (s *Session) serve() {
	for {
		select {
		case <-s.ctx.Done():
			return
		case req := <-s.inbox:
			m := req.msg
			if m.ID == "" || len(m.ID) > 64 || l3.Protocol(m.Return) == 0 {
				continue
			}
			s.mu.Lock()
			cached, ok := s.cache[m.ID]
			s.mu.Unlock()
			if ok {
				_ = s.send(m.Return, cached)
				continue
			}
			body, e := s.handle(m, req.carrier)
			reply := message{Reply: m.ID, Op: m.Op, Trial: m.Trial}
			if e != nil {
				reply.Error = e.Error()
			} else {
				reply.Body, _ = json.Marshal(body)
			}
			s.mu.Lock()
			if len(s.cache) >= 128 {
				s.cache = map[string]message{}
			}
			s.cache[m.ID] = reply
			s.mu.Unlock()
			_ = s.send(m.Return, reply)
		}
	}
}
func newTrial(index int, o Options, send, receive string) *trial {
	n := (o.count() + 63) / 64
	return &trial{index: index, options: o, send: send, receive: receive, up: counter{bits: make([]uint64, n)}, down: counter{bits: make([]uint64, n)}}
}
func (s *Session) handle(m message, carrier string) (any, error) {
	if m.Op == "record" {
		var result Result
		if json.Unmarshal(m.Body, &result) != nil || result.Index != m.Trial || result.Index < 0 || result.Index >= 25 || result.Send != Carriers[result.Index/5] || result.Receive != Carriers[result.Index%5] {
			return nil, errors.New("invalid result")
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		found := false
		for i := range s.view.Results {
			if s.view.Results[i].Index == result.Index {
				s.view.Results[i] = result
				found = true
			}
		}
		if !found {
			s.view.Results = append(s.view.Results, result)
		}
		s.view.Progress = len(s.view.Results) * 100 / 25
		return true, nil
	}

	if m.Op == "prepare" {
		var o Options
		if json.Unmarshal(m.Body, &o) != nil || o.Validate() != nil || m.Trial < 0 || m.Trial >= 25 || carrier != Carriers[m.Trial/5] || m.Return != Carriers[m.Trial%5] {
			return nil, errors.New("invalid trial")
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.current != nil && m.Trial < s.current.index {
			return nil, errors.New("old trial")
		}
		if s.current == nil || m.Trial != s.current.index {
			s.current = newTrial(m.Trial, o, carrier, m.Return)
		}
		s.view.Options = o
		s.view.Status = "running"
		s.view.Current = m.Trial
		s.view.Phase = "testing"
		return true, nil
	}
	if m.Op == "done" {
		s.mu.Lock()
		s.view.Status = "done"
		s.view.Phase = ""
		s.view.Progress = 100
		s.view.Recommended = Recommend(s.view.Results)
		s.mu.Unlock()
		return true, nil
	}
	if m.Op == "apply_prepare" || m.Op == "apply_commit" || m.Op == "apply_rollback" || m.Op == "apply_finalize" || m.Op == "apply_status" {
		s.mu.Lock()
		valid := false
		for _, r := range s.view.Results {
			if r.Index == m.Trial && r.Status == "passed" && r.Send == carrier && r.Receive == m.Return {
				valid = true
			}
		}
		s.mu.Unlock()
		if !valid {
			return nil, errors.New("carrier pair has no successful recorded benchmark")
		}
		if s.hook == nil {
			return nil, errors.New("applying changes is disabled on the other server")
		}
		return s.hook(s.ctx, m.Op, m.Body)
	}
	s.mu.Lock()
	t := s.current
	if t == nil || t.index != m.Trial || carrier != t.send || m.Return != t.receive {
		s.mu.Unlock()
		return nil, errors.New("trial not prepared")
	}
	switch m.Op {
	case "ping":
		s.mu.Unlock()
		return true, nil
	case "upload_report":
		n := t.up.count
		s.mu.Unlock()
		return n, nil
	case "download":
		if !t.downloadStarted {
			t.downloadStarted = true
			s.wg.Add(1)
			go func() {
				defer s.wg.Done()
				d := s.emit(t, 1, t.receive)
				s.mu.Lock()
				t.download = d
				t.downloadDone = true
				s.mu.Unlock()
			}()
		}
		s.mu.Unlock()
		return true, nil
	case "download_report":
		out := struct {
			Done      bool      `json:"done"`
			Direction Direction `json:"direction"`
		}{t.downloadDone, t.download}
		s.mu.Unlock()
		return out, nil
	case "done":
		s.view.Status = "done"
		s.view.Phase = ""
		s.view.Progress = 100
		s.view.Recommended = Recommend(s.view.Results)
		s.mu.Unlock()
		return true, nil
	default:
		s.mu.Unlock()
		return nil, errors.New("unsupported benchmark operation")
	}
}
func (s *Session) emit(t *trial, dir byte, carrier string) Direction {
	count := t.options.count()
	start := time.Now()
	duration := time.Duration(t.options.Seconds) * time.Second
	d := Direction{}
	payload := make([]byte, payloadBytes)
	binary.BigEndian.PutUint16(payload, uint16(t.index))
	payload[2] = dir
	// Random bytes prevent compressible test payloads from flattering goodput.
	_, _ = rand.Read(payload[8:])
	timer := time.NewTimer(0)
	defer timer.Stop()
	for seq := 0; seq < count; seq++ {
		if seq%16 == 0 {
			due := start.Add(time.Duration(int64(seq) * int64(duration) / int64(count)))
			wait := time.Until(due)
			if wait > 0 {
				timer.Reset(wait)
				select {
				case <-timer.C:
				case <-s.ctx.Done():
					d.Seconds = time.Since(start).Seconds()
					return d
				}
			}
			if s.ctx.Err() != nil {
				break
			}
		}
		binary.BigEndian.PutUint32(payload[4:], uint32(seq))
		d.Sent++
		if e := s.wires[carrier].Send(s.bulk[carrier].Seal(dataKind, payload)); e != nil {
			d.SendErrors++
		}
	}
	d.Seconds = math.Max(time.Since(start).Seconds(), float64(t.options.Seconds))
	return d
}
func measure(d Direction, received, rate int) Direction {
	d.Received = received
	if d.Sent > 0 {
		d.LossPct = 100 * float64(d.Sent-received) / float64(d.Sent)
		if d.LossPct < 0 {
			d.LossPct = 0
		}
	}
	if d.Seconds > 0 {
		d.Mbps = float64(received*payloadBytes*8) / d.Seconds / 1e6
	}
	d.Capped = d.Mbps >= .95*float64(rate)
	return d
}
func Recommend(results []Result) int {
	best := -1
	score := -1.0
	for _, r := range results {
		if r.Status != "passed" || r.PingReplies < 8 || r.Upload.Received == 0 || r.Download.Received == 0 {
			continue
		}
		if r.Score > score {
			score = r.Score
			best = r.Index
		}
	}
	return best
}
func (s *Session) run(o Options) {
	s.mu.Lock()
	s.view.Status = "running"
	s.mu.Unlock()
	lastResponsive := -1
	for index := 0; index < 25 && s.ctx.Err() == nil; index++ {
		a, b := Carriers[index/5], Carriers[index%5]
		r := Result{Index: index, Send: a, Receive: b, Status: "failed"}
		s.mu.Lock()
		s.view.Current = index
		s.view.Phase = "handshake"
		s.current = newTrial(index, o, a, b)
		t := s.current
		s.mu.Unlock()
		if _, e := s.rpc(s.ctx, a, b, "prepare", index, o, 4); e != nil {
			r.Error = e.Error()
			s.record(r)
			continue
		}
		lastResponsive = index
		s.mu.Lock()
		s.view.Phase = "latency"
		s.mu.Unlock()
		samples := []float64{}
		for n := 0; n < 12 && s.ctx.Err() == nil; n++ {
			r.PingSent++
			start := time.Now()
			if _, e := s.rpc(s.ctx, a, b, "ping", index, nil, 1); e == nil {
				samples = append(samples, float64(time.Since(start).Microseconds())/1000)
				r.PingReplies++
			}
			select {
			case <-time.After(40 * time.Millisecond):
			case <-s.ctx.Done():
			}
		}
		if len(samples) < 8 {
			r.Error = "fewer than 8 of 12 authenticated RTT samples"
			s.record(r)
			continue
		}
		sort.Float64s(samples)
		r.RTTMs = samples[len(samples)/2]
		if len(samples)%2 == 0 {
			r.RTTMs = (samples[len(samples)/2-1] + r.RTTMs) / 2
		}
		r.P95Ms = samples[int(math.Ceil(.95*float64(len(samples))))-1]
		s.mu.Lock()
		s.view.Phase = "upload"
		s.mu.Unlock()
		r.Upload = s.emit(t, 0, a)
		grace := time.Duration(math.Max(200, math.Min(2000, 2*r.P95Ms))) * time.Millisecond
		select {
		case <-time.After(grace):
		case <-s.ctx.Done():
			return
		}
		body, e := s.rpc(s.ctx, a, b, "upload_report", index, nil, 4)
		var n int
		if e != nil || json.Unmarshal(body, &n) != nil || n < 0 || n > r.Upload.Sent {
			r.Error = "upload receipt report missing or invalid"
			s.record(r)
			continue
		}
		r.Upload = measure(r.Upload, n, o.RateMbps)
		s.mu.Lock()
		s.view.Phase = "download"
		s.mu.Unlock()
		if _, e = s.rpc(s.ctx, a, b, "download", index, nil, 4); e != nil {
			r.Error = e.Error()
			s.record(r)
			continue
		}
		deadline := time.Now().Add(time.Duration(o.Seconds)*time.Second + 15*time.Second)
		done := false
		for !done && time.Now().Before(deadline) && s.ctx.Err() == nil {
			select {
			case <-time.After(250 * time.Millisecond):
			case <-s.ctx.Done():
				return
			}
			body, e = s.rpc(s.ctx, a, b, "download_report", index, nil, 2)
			if e != nil {
				continue
			}
			var report struct {
				Done      bool      `json:"done"`
				Direction Direction `json:"direction"`
			}
			if json.Unmarshal(body, &report) == nil && report.Done {
				r.Download = report.Direction
				done = true
			}
		}
		if !done {
			r.Error = "download report timed out"
			s.record(r)
			continue
		}
		select {
		case <-time.After(grace):
		case <-s.ctx.Done():
			return
		}
		s.mu.Lock()
		n = t.down.count
		s.mu.Unlock()
		if n > r.Download.Sent {
			r.Error = "invalid download receipt count"
			s.record(r)
			continue
		}
		r.Download = measure(r.Download, n, o.RateMbps)
		if r.Upload.Received == 0 || r.Download.Received == 0 {
			r.Error = "no delivered payload in one or both directions"
		} else {
			r.Status = "passed"
			loss := math.Max(r.Upload.LossPct, r.Download.LossPct)
			pingLoss := 100 * float64(r.PingSent-r.PingReplies) / float64(r.PingSent)
			r.Score = math.Min(r.Upload.Mbps, r.Download.Mbps) * math.Pow(1-math.Max(loss, pingLoss)/100, 2) / (1 + r.P95Ms/200)
		}
		s.record(r)
		_, _ = s.rpc(s.ctx, a, b, "record", index, r, 3)
	}
	if s.ctx.Err() != nil {
		return
	}
	v := s.Status()
	best := Recommend(v.Results)
	route := best
	if route < 0 {
		route = lastResponsive
	}
	if route >= 0 {
		a, b := Carriers[route/5], Carriers[route%5]
		s.mu.Lock()
		s.view.Phase = "finishing"
		s.mu.Unlock()
		for _, r := range v.Results {
			if s.ctx.Err() != nil {
				return
			}
			_, _ = s.rpc(s.ctx, a, b, "record", r.Index, r, 2)
		}
		_, _ = s.rpc(s.ctx, a, b, "done", route, nil, 4)
	}
	if s.ctx.Err() != nil {
		return
	}
	s.mu.Lock()
	s.view.Status = "done"
	s.view.Phase = ""
	s.view.Progress = 100
	s.view.Recommended = best
	s.mu.Unlock()

}
func (s *Session) record(r Result) {
	s.mu.Lock()
	s.view.Results = append(s.view.Results, r)
	s.view.Progress = (r.Index + 1) * 100 / 25
	s.mu.Unlock()
}

// RemoteApply sends a narrowly scoped request over the tested carrier pair.
// Callers must present their own concrete confirmation UI before invoking it.
func (s *Session) RemoteApply(ctx context.Context, r Result, op string, body any) (json.RawMessage, error) {
	return s.rpc(ctx, r.Send, r.Receive, op, r.Index, body, 32)
}
func (s *Session) BeginApply(index int) (Result, error) {
	s.rpcMu.Lock()
	defer s.rpcMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.view.Role != "local" || s.view.Status != "done" || s.ctx.Err() != nil || s.view.Applied {
		return Result{}, errors.New("benchmark is not ready to apply")
	}
	for _, r := range s.view.Results {
		if r.Index == index && r.Status == "passed" && r.Score > 0 {
			s.view.Status = "applying"
			s.view.Phase = "prepare"
			return r, nil
		}
	}
	return Result{}, errors.New("choose a successful measured carrier pair")
}
func (s *Session) EndApply(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.view.Status = "done"
	s.view.Phase = ""
	if err != nil {
		s.view.Error = err.Error()
	} else {
		s.view.Error = ""
		s.view.Applied = true
	}
}

// SetRemoteApply keeps the receiving panel in sync with its scoped apply hook.
func (s *Session) SetRemoteApply(pending, applied bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.view.Role != "remote" {
		return
	}
	s.view.Status = "done"
	s.view.Phase = ""
	s.view.Applied = applied
	if pending {
		s.view.Status = "applying"
		s.view.Phase = "prepare"
	}
	if err != nil {
		s.view.Error = err.Error()
	} else {
		s.view.Error = ""
	}
}
