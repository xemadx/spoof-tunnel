package l3

import (
	"container/heap"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math"
	"math/bits"
	"net"
	"sync"
	"time"
)

// PoolPolicy is per link. Empty mode preserves the old packet-by-packet rotation.
type PoolPolicy struct {
	ActiveSize      int    `json:"active_size,omitempty"`
	RefreshSeconds  int    `json:"refresh_seconds,omitempty"`
	Mode            string `json:"mode"`
	ProbePPS        int    `json:"probe_pps"`
	CooldownSeconds int    `json:"cooldown_seconds"`
}

func (p PoolPolicy) Normalized() PoolPolicy {
	if p.RefreshSeconds == 0 {
		p.RefreshSeconds = 1800
	}
	if p.Mode == "" {
		p.Mode = "legacy"
	}
	if p.ProbePPS == 0 {
		p.ProbePPS = 25
	}
	if p.CooldownSeconds == 0 {
		p.CooldownSeconds = 600
	}
	return p
}
func (p PoolPolicy) Validate() error {
	p = p.Normalized()
	switch p.Mode {
	case "legacy", "adaptive", "round_robin", "failover":
	default:
		return fmt.Errorf("unknown pool mode")
	}
	if p.ProbePPS < 1 || p.ProbePPS > 100 {
		return fmt.Errorf("probe rate must be 1–100 packets/second")
	}
	if p.ActiveSize < 0 || p.ActiveSize > 400 {
		return fmt.Errorf("active source count must be 0–400")
	}
	if p.ActiveSize > 0 && p.Mode == "legacy" {
		return fmt.Errorf("active source set requires a smart dispatch mode")
	}
	if p.ActiveSize > 4*p.ProbePPS {
		return fmt.Errorf("active source count exceeds the verification budget (maximum 4 × probe rate)")
	}
	if p.RefreshSeconds < 60 || p.RefreshSeconds > 86400 {
		return fmt.Errorf("source-set refresh must be 60–86400 seconds")
	}
	if p.CooldownSeconds != 600 {
		return fmt.Errorf("pool quarantine must be 600 seconds")
	}
	return nil
}

type SourceQuality struct {
	Selected     bool    `json:"selected"`
	Reason       string  `json:"reason,omitempty"`
	IP           string  `json:"ip"`
	State        string  `json:"state"`
	RTTMs        float64 `json:"rtt_ms"`
	JitterMs     float64 `json:"jitter_ms,omitempty"`
	LossPct      float64 `json:"loss_pct"`
	Probes       uint64  `json:"probes"`
	Replies      uint64  `json:"replies"`
	Failures     int     `json:"failures"`
	RetryAt      int64   `json:"retry_at,omitempty"`
	LastSuccess  int64   `json:"last_success,omitempty"`
	recent       uint8
	observations int
	ready        bool
	pending      bool
	lastAck      time.Time
	nextProbe    time.Time
	validUntil   time.Time
	streak       int
	version      uint64
}
type PoolView struct {
	Selected    int             `json:"selected"`
	ActiveSize  int             `json:"active_size"`
	RefreshAt   int64           `json:"refresh_at,omitempty"`
	Generation  uint64          `json:"generation"`
	Rebuilding  bool            `json:"rebuilding"`
	Mode        string          `json:"mode"`
	Total       int             `json:"total"`
	Healthy     int             `json:"healthy"`
	Testing     int             `json:"testing"`
	Quarantined int             `json:"quarantined"`
	Unknown     int             `json:"unknown"`
	Active      string          `json:"active,omitempty"`
	Offset      int             `json:"offset"`
	Items       []SourceQuality `json:"items"`
}
type poolDue struct {
	at      time.Time
	index   int
	version uint64
}
type poolQueue []poolDue

func (q poolQueue) Len() int           { return len(q) }
func (q poolQueue) Less(i, j int) bool { return q[i].at.Before(q[j].at) }
func (q poolQueue) Swap(i, j int)      { q[i], q[j] = q[j], q[i] }
func (q *poolQueue) Push(x any)        { *q = append(*q, x.(poolDue)) }
func (q *poolQueue) Pop() any          { a := *q; x := a[len(a)-1]; *q = a[:len(a)-1]; return x }

type sourceProbe struct {
	index    int
	sent     time.Time
	deadline time.Time
}

// SourcePool keeps packet selection O(1). Work and pending probes are bounded.
type SourcePool struct {
	verified     []int
	verifiedPos  map[int]int
	cohort       *sourceCohort
	mu           sync.Mutex
	referenceRTT time.Duration
	policy       PoolPolicy
	states       []SourceQuality
	ips          []net.IP
	positions    map[string]int
	pending      map[string]sourceProbe
	due          poolQueue
	fresh        int
	turn         uint64
	rr           uint64
	good         []int
	goodPos      map[int]int
	best         int
	first        int
	active       int
}

func NewSourcePool(ips []net.IP, policy PoolPolicy) *SourcePool {
	p := &SourcePool{policy: policy.Normalized(), positions: map[string]int{}, pending: map[string]sourceProbe{}, goodPos: map[int]int{}, verifiedPos: map[int]int{}, best: -1, active: -1}
	for _, ip := range ips {
		key := ip.String()
		if _, ok := p.positions[key]; ok {
			continue
		}
		p.positions[key] = len(p.ips)
		p.ips = append(p.ips, append(net.IP(nil), ip.To4()...))
		p.states = append(p.states, SourceQuality{IP: key, State: "unknown"})
	}
	if p.policy.ActiveSize > 0 {
		p.cohort = newSourceCohort()
	}
	return p
}
func (p *SourcePool) schedule(i int, at time.Time) {
	p.states[i].version++
	p.states[i].nextProbe = at
	heap.Push(&p.due, poolDue{at, i, p.states[i].version})
}
func removePoolIndex(items *[]int, positions map[int]int, i int) {
	if pos, ok := positions[i]; ok {
		last := (*items)[len(*items)-1]
		(*items)[pos] = last
		positions[last] = pos
		*items = (*items)[:len(*items)-1]
		delete(positions, i)
	}
}
func (p *SourcePool) addSelected(i int) {
	if _, ok := p.goodPos[i]; !ok {
		p.goodPos[i] = len(p.good)
		p.good = append(p.good, i)
	}
}
func (p *SourcePool) removeGood(i int) {
	removePoolIndex(&p.good, p.goodPos, i)
	removePoolIndex(&p.verified, p.verifiedPos, i)
	p.states[i].ready = false
	p.recomputeBest()
}
func (p *SourcePool) recomputeBest() {
	previous := p.best
	p.best = -1
	p.first = -1
	for _, i := range p.good {
		if p.first < 0 || i < p.first {
			p.first = i
		}
		if p.best < 0 || p.score(i) < p.score(p.best) {
			p.best = i
		}
	}
	if previous >= 0 && p.isSelected(previous) && p.states[previous].ready && p.best >= 0 && p.score(previous) <= p.score(p.best)*1.15+5 {
		p.best = previous
	}
}
func (p *SourcePool) score(i int) float64 { s := p.states[i]; return s.RTTMs + 2.0*s.JitterMs + 20*s.LossPct }
func (p *SourcePool) Select(control bool) (net.IP, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.ips) == 0 {
		return nil, fmt.Errorf("empty source pool")
	}
	var i int
	if p.policy.Mode == "legacy" {
		i = int(p.rr % uint64(len(p.ips)))
		p.rr++
	} else if len(p.good) == 0 {
		if !control {
			return nil, fmt.Errorf("no verified healthy source; discovery in progress")
		}
		found := false
		for tries := 0; tries < len(p.ips); tries++ {
			i = int(p.rr % uint64(len(p.ips)))
			p.rr++
			if p.states[i].State != "quarantined" {
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("all sources are quarantined; waiting for retest")
		}
	} else if control {
		i = p.best
	} else {
		switch p.policy.Mode {
		case "round_robin":
			i = p.good[p.rr%uint64(len(p.good))]
			p.rr++
		case "failover":
			if p.active >= 0 && p.states[p.active].ready && p.isSelected(p.active) {
				i = p.active
			} else {
				i = p.first
			}
		default:
			i = p.best
		}
	}
	if !control {
		p.active = i
	}
	return append(net.IP(nil), p.ips[i]...), nil
}

// ProvenFallback returns the best non-quarantined source that has previously
// passed authenticated round-trip verification (LastSuccess > 0). Used when
// an established link's active pool temporarily expires during re-probing so
// user data continues flowing without admitting untested or quarantined IPs.
func (p *SourcePool) ProvenFallback() (net.IP, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	best := -1
	for i, s := range p.states {
		if s.State == "quarantined" || s.LastSuccess == 0 {
			continue
		}
		if best < 0 || s.LastSuccess > p.states[best].LastSuccess || (s.LastSuccess == p.states[best].LastSuccess && p.score(i) < p.score(best)) {
			best = i
		}
	}
	if best < 0 {
		return nil, false
	}
	p.active = best
	return append(net.IP(nil), p.ips[best]...), true
}
func (p *SourcePool) NextProbe(now time.Time) (string, net.IP) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cohort != nil {
		p.cohort.tick(p, now)
	}
	if p.policy.Mode == "legacy" || len(p.pending) >= 128 {
		return "", nil
	}
	// Two new-address slots for each retest slot prevent a large quarantine
	// backlog from starving discovery. Once discovery ends all slots retest.
	for p.fresh < len(p.states) && p.states[p.fresh].RetryAt > 0 {
		p.fresh++
	}
	p.turn++
	i := -1
	if p.cohort != nil {
		i = p.cohort.maintenance(p, now)
	} else {
		// Keep the data source and control reference tested ahead of a bulk scan.
		for _, candidate := range []int{p.active, p.best} {
			if candidate >= 0 {
				q := p.states[candidate]
				if q.ready && !q.pending && !q.nextProbe.After(now) {
					i = candidate
					break
				}
			}
		}
		if i < 0 {
			selected := -1
			for _, candidate := range p.good {
				q := p.states[candidate]
				if q.ready && !q.pending && !q.nextProbe.After(now) && (selected < 0 || q.nextProbe.Before(p.states[selected].nextProbe)) {
					selected = candidate
				}
			}
			i = selected
		}
	}
	// Complete small/MTU confirmation pairs before opening more candidates.
	for len(p.due) > 0 && p.states[p.due[0].index].version != p.due[0].version {
		heap.Pop(&p.due)
	}
	if i < 0 && len(p.due) > 0 && !p.due[0].at.After(now) {
		candidate := p.due[0].index
		q := p.states[candidate]
		if !q.ready && !q.pending && (q.streak > 0 || q.LastSuccess > 0) {
			heap.Pop(&p.due)
			i = candidate
		}
	}
	if len(p.pending) >= 126 && (i < 0 || !p.isSelected(i)) {
		return "", nil
	}
	if i >= 0 {
		// Reserve two outstanding slots for the active and reference sources.
	} else if p.fresh < len(p.states) && (p.turn%3 != 0 || len(p.due) == 0 || p.due[0].at.After(now)) {
		i = p.fresh
		p.fresh++
	} else {
		for len(p.due) > 0 && !p.due[0].at.After(now) {
			d := heap.Pop(&p.due).(poolDue)
			if p.states[d.index].version == d.version && !p.states[d.index].pending {
				i = d.index
				break
			}
		}
		if i < 0 && p.fresh < len(p.states) {
			i = p.fresh
			p.fresh++
		}
	}
	if i < 0 {
		return "", nil
	}
	if p.states[i].State == "quarantined" {
		p.states[i].Reason = "cooldown_retest"
		p.states[i].recent = 0
		p.states[i].observations = 0
		p.states[i].Failures = 0
		p.states[i].streak = 0
	}
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		p.schedule(i, now.Add(time.Second))
		return "", nil
	}
	id := hex.EncodeToString(token[:])
	p.pending[id] = sourceProbe{i, now, now.Add(p.probeTimeout(i))}
	s := &p.states[i]
	s.pending = true
	s.version++
	s.Probes++
	if !s.ready {
		s.State = "testing"
		if s.Reason != "cooldown_retest" {
			s.Reason = "awaiting_confirmation"
		}
	}
	s.RetryAt = 0
	return id, append(net.IP(nil), p.ips[i]...)
}

// ReferenceRTT is updated only by authenticated, nonce-bound round trips.
func (p *SourcePool) ReferenceRTT(rtt time.Duration) {
	if rtt <= 0 {
		return
	}
	p.mu.Lock()
	p.referenceRTT = rtt
	p.mu.Unlock()
}
func (p *SourcePool) probeTimeout(i int) time.Duration {
	timeout := 3 * time.Second
	if p.states[i].RTTMs == 0 {
		timeout = 15 * time.Second
	}
	for _, rtt := range []time.Duration{p.referenceRTT, time.Duration(p.states[i].RTTMs * float64(time.Millisecond))} {
		if d := 4*rtt + time.Second; d > timeout {
			timeout = d
		}
	}
	if timeout > time.Minute {
		timeout = time.Minute
	}
	return timeout
}
func (p *SourcePool) probeDeadline(probe sourceProbe) time.Time {
	deadline := probe.sent.Add(p.probeTimeout(probe.index))
	if deadline.Before(probe.deadline) {
		return probe.deadline
	}
	return deadline
}
func (p *SourcePool) Acknowledge(token, observed string, now time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	probe, ok := p.pending[token]
	if !ok || p.states[probe.index].IP != observed || now.After(p.probeDeadline(probe)) {
		return false
	}
	delete(p.pending, token)
	s := &p.states[probe.index]
	s.pending = false
	s.Replies++
	staleMeasurement := !s.lastAck.IsZero() && now.Sub(s.lastAck) > p.qualityLifetime(probe.index)
	if staleMeasurement {
		s.streak = 0
		s.recent = 0
		s.observations = 0
		s.LossPct = 0
		s.RTTMs = 0
		s.JitterMs = 0
	}
	s.lastAck = now
	p.referenceRTT = now.Sub(probe.sent)
	recordPoolOutcome(s, false)
	s.Failures = 0
	s.streak++
	s.LastSuccess = now.Unix()
	s.RetryAt = 0
	rtt := float64(now.Sub(probe.sent).Microseconds()) / 1000
	if s.RTTMs == 0 {
		s.RTTMs = rtt
		s.JitterMs = 0
	} else {
		diff := math.Abs(rtt - s.RTTMs)
		if s.JitterMs == 0 {
			s.JitterMs = diff
		} else {
			s.JitterMs = .8*s.JitterMs + .2*diff
		}
		s.RTTMs = .8*s.RTTMs + .2*rtt
	}
	s.LossPct *= .8
	lifetime := p.qualityLifetime(probe.index)
	s.validUntil = now.Add(lifetime)
	if s.streak >= 2 || s.ready {
		s.State = "healthy"
		s.Reason = ""
		if bits.OnesCount8(s.recent) > 0 {
			s.State = "suspect"
			s.Reason = "recent_probe_loss"
		}
		if !s.ready {
			s.ready = true
			p.verifiedPos[probe.index] = len(p.verified)
			p.verified = append(p.verified, probe.index)
		}
		if p.cohort == nil || len(p.good) < p.policy.ActiveSize {
			p.addSelected(probe.index)
		}
		if p.cohort != nil {
			p.cohort.observe(p, probe.index)
		}
		p.recomputeBest()
		p.schedule(probe.index, p.nextQualityProbe(probe.index, now))
	} else {
		s.State = "testing"
		s.Reason = "awaiting_confirmation"
		p.schedule(probe.index, now.Add(100*time.Millisecond))
	}
	return true
}
func (p *SourcePool) Expire(now time.Time, reverseFresh bool) (changed bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	// Forget stale quality without treating an untested source as failed.
	// Batch removal keeps this maintenance O(healthy sources), outside data I/O.
	stale := false
	for pos := 0; pos < len(p.verified); {
		i := p.verified[pos]
		s := &p.states[i]
		if now.Before(s.validUntil) {
			pos++
			continue
		}
		wasSelected := p.isSelected(i)
		removePoolIndex(&p.verified, p.verifiedPos, i)
		removePoolIndex(&p.good, p.goodPos, i)
		s.ready = false
		s.streak = 0
		s.State = "unknown"
		s.Reason = "verification_expired"
		if p.cohort != nil {
			p.cohort.observe(p, i)
		}
		if !s.pending && (p.cohort == nil || wasSelected || p.cohort.needs(p, i)) {
			p.schedule(i, now)
		}
		stale = true
	}
	if stale {
		p.recomputeBest()
	}
	for token, probe := range p.pending {
		if now.Before(p.probeDeadline(probe)) {
			continue
		}
		delete(p.pending, token)
		s := &p.states[probe.index]
		s.pending = false
		if !reverseFresh {
			s.Reason = "reverse_unconfirmed"
			if !s.ready {
				s.State = "unknown"
			}
			p.schedule(probe.index, now.Add(5*time.Second))
			continue
		}
		s.Failures++
		s.Reason = "probe_timeout"
		if s.Probes%2 == 0 {
			s.Reason = "large_probe_timeout"
		}
		recordPoolOutcome(s, true)
		s.streak = 0
		s.LossPct = .8*s.LossPct + 20
		if s.ready {
			p.recomputeBest()
		}
		if s.Failures >= 3 || (s.observations >= 5 && bits.OnesCount8(s.recent) >= 3) {
			changed = true
			p.removeGood(probe.index)
			if p.cohort != nil {
				p.cohort.forget(probe.index)
			}
			s.State = "quarantined"
			if s.Failures < 3 {
				s.Reason = "recent_probe_loss"
			}
			deadline := now.Add(time.Duration(p.policy.CooldownSeconds) * time.Second)
			s.RetryAt = deadline.Unix()
			if deadline.Nanosecond() != 0 {
				s.RetryAt++
			}
			p.schedule(probe.index, deadline)
		} else {
			if s.ready {
				s.State = "suspect"
				p.schedule(probe.index, now.Add(time.Second))
			} else {
				s.State = "testing"
				p.schedule(probe.index, now.Add(100*time.Millisecond))
			}
		}
	}
	if p.cohort != nil {
		p.cohort.tick(p, now)
		p.cohort.refill(p, now)
	}
	return changed
}
func (p *SourcePool) View(offset, limit int, filter string) PoolView {
	p.mu.Lock()
	defer p.mu.Unlock()
	if offset < 0 {
		offset = 0
	}
	v := PoolView{Mode: p.policy.Mode, Total: len(p.states), Selected: len(p.good), ActiveSize: p.policy.ActiveSize, Offset: offset, Items: []SourceQuality{}}
	if p.cohort != nil {
		v.Generation = p.cohort.generation
		if !p.cohort.refreshAt.IsZero() {
			v.RefreshAt = p.cohort.refreshAt.Unix()
		}
		v.Rebuilding = p.cohort.warming
	}
	if p.active >= 0 && (p.policy.Mode == "legacy" || p.states[p.active].ready && p.isSelected(p.active)) {
		v.Active = p.states[p.active].IP
	}
	if limit < 1 || limit > 100 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	matched := 0
	for _, s := range p.states {
		switch s.State {
		case "healthy", "suspect":
			v.Healthy++
		case "quarantined":
			v.Quarantined++
		case "testing":
			v.Testing++
		default:
			v.Unknown++
		}
		s.Selected = p.isSelected(p.positions[s.IP])
		if filter == "selected" && !s.Selected {
			continue
		}
		if filter != "" && filter != "selected" && s.State != filter {
			continue
		}
		if matched >= offset && len(v.Items) < limit {
			v.Items = append(v.Items, s)
		}
		matched++
	}
	return v
}

// Checkpoints contain only quarantine deadlines. A restart never promotes a
// source based on stale historical health and never extends a quarantine.
func (p *SourcePool) Checkpoint() map[string]int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := map[string]int64{}
	for _, s := range p.states {
		if s.RetryAt > 0 {
			out[s.IP] = s.RetryAt
		}
	}
	return out
}
func (p *SourcePool) Restore(deadlines map[string]int64, now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for ip, until := range deadlines {
		if i, ok := p.positions[ip]; ok && until > now.Unix() {
			remaining := until - now.Unix()
			if remaining > 600 {
				remaining = 600
			}
			deadline := now.Add(time.Duration(remaining) * time.Second)
			s := &p.states[i]
			s.State = "quarantined"
			s.Reason = "restored_quarantine"
			s.RetryAt = deadline.Unix()
			if deadline.Nanosecond() != 0 {
				s.RetryAt++
			}
			p.schedule(i, deadline)
		}
	}
}

// SeedVerified pre-marks one source as verified, as if it had just
// passed admission (two consecutive probe acks). It is used exactly once
// at startup for a source a benchmark has already proven with the same
// carriers: the tunnel then does not have to rediscover from scratch
// before its first authenticated heartbeat can count toward readiness.
// The seed is self-correcting — the normal quality probe is scheduled
// immediately and Expire drops the source again if it stops answering —
// and it never overrides a quarantine. Returns false when the IP is not
// in the pool or is quarantined.
func (p *SourcePool) SeedVerified(ip net.IP, validFor time.Duration) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	i, ok := p.positions[ip.String()]
	if !ok {
		return false
	}
	s := &p.states[i]
	if s.State == "quarantined" {
		return false
	}
	now := time.Now()
	s.streak = 2
	s.ready = true
	s.State = "healthy"
	s.Reason = "benchmark_seeded"
	s.recent = 0
	s.observations = 2
	s.Failures = 0
	s.RetryAt = 0
	s.LastSuccess = now.Unix()
	s.validUntil = now.Add(validFor)
	if _, ok := p.verifiedPos[i]; !ok {
		p.verifiedPos[i] = len(p.verified)
		p.verified = append(p.verified, i)
	}
	if p.cohort == nil || len(p.good) < p.policy.ActiveSize {
		p.addSelected(i)
	}
	if p.cohort != nil {
		p.cohort.observe(p, i)
	}
	p.recomputeBest()
	p.schedule(i, p.nextQualityProbe(i, now))
	return true
}

// ProbeSize alternates small control-sized frames and full configured inner MTU.
func (p *SourcePool) ProbeSize(token string, mtu int) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	if q, ok := p.pending[token]; ok && p.states[q.index].Probes%2 == 0 && mtu >= 576 && mtu <= 1300 {
		return mtu - 12
	}
	return 64
}

func (p *SourcePool) Ready() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.policy.Mode == "legacy" || len(p.good) > 0
}

func recordPoolOutcome(s *SourceQuality, miss bool) {
	s.recent = (s.recent << 1) & 63
	if miss {
		s.recent |= 1
	}
	if s.observations < 6 {
		s.observations++
	}
}

func (p *SourcePool) isSelected(i int) bool { _, ok := p.goodPos[i]; return ok }
func (p *SourcePool) maintenanceInterval() time.Duration {
	d := 10 * time.Second
	size := p.policy.ActiveSize
	if size <= 0 {
		size = len(p.good)
	}
	if size > 0 && p.policy.ProbePPS > 0 {
		if n := time.Duration(4*size) * time.Second / time.Duration(p.policy.ProbePPS); n > d {
			d = n
		}
	}
	return d
}
func (p *SourcePool) qualityLifetime(i int) time.Duration {
	d := 3 * p.probeTimeout(i)
	if d < 30*time.Second {
		d = 30 * time.Second
	}
	if n := 3 * p.maintenanceInterval(); n > d {
		d = n
	}
	return d
}
func (p *SourcePool) nextQualityProbe(i int, now time.Time) time.Time {
	if p.cohort == nil || p.isSelected(i) || p.cohort.needs(p, i) {
		return now.Add(p.maintenanceInterval())
	}
	if p.cohort.standbyHas(i) && p.cohort.preflightAt.After(now) {
		return p.cohort.preflightAt
	}
	return now.Add(time.Duration(p.policy.RefreshSeconds) * time.Second)
}
