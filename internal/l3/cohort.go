package l3

import (
	"sort"
	"time"
)

// sourceCohort is owned by SourcePool.mu. Only good contains data sources;
// standby retains bounded historical candidates for refresh preflight.
type sourceCohort struct {
	standby                map[int]bool
	refreshAt, preflightAt time.Time
	generation             uint64
	warming                bool
	turn                   uint64
}

func newSourceCohort() *sourceCohort          { return &sourceCohort{standby: map[int]bool{}} }
func (c *sourceCohort) standbyHas(i int) bool { return c.standby[i] }
func (c *sourceCohort) needs(p *SourcePool, i int) bool {
	return c.standby[i] && (c.warming || len(p.good) < p.policy.ActiveSize)
}
func (c *sourceCohort) forget(i int) { delete(c.standby, i) }
func cohortScore(p *SourcePool, i int) float64 {
	s := p.states[i]
	return p.score(i) + float64(6-s.observations)*5
}
func cohortLess(p *SourcePool, a, b int) bool {
	x, y := cohortScore(p, a), cohortScore(p, b)
	return x < y || (x == y && a < b)
}
func (c *sourceCohort) observe(p *SourcePool, i int) {
	if p.isSelected(i) || p.states[i].State == "quarantined" || p.states[i].LastSuccess == 0 {
		delete(c.standby, i)
		return
	}
	c.standby[i] = true
	if len(c.standby) > p.policy.ActiveSize {
		worst := -1
		for j := range c.standby {
			if worst < 0 || cohortLess(p, worst, j) {
				worst = j
			}
		}
		delete(c.standby, worst)
	}
}
func (c *sourceCohort) arm(p *SourcePool, now time.Time) {
	interval := time.Duration(p.policy.RefreshSeconds) * time.Second
	lead := time.Duration(24*p.policy.ActiveSize) * time.Second / time.Duration(p.policy.ProbePPS)
	if lead < 30*time.Second {
		lead = 30 * time.Second
	}
	if lead > interval/2 {
		lead = interval / 2
	}
	c.refreshAt = now.Add(interval)
	c.preflightAt = c.refreshAt.Add(-lead)
	c.warming = false
}
func (c *sourceCohort) tick(p *SourcePool, now time.Time) {
	if c.refreshAt.IsZero() {
		c.generation = 1
		c.arm(p, now)
	}
	if !c.warming && !now.Before(c.preflightAt) {
		c.warming = true
		for i := range c.standby {
			if !p.states[i].pending {
				p.schedule(i, now)
			}
		}
	}
	if now.Before(c.refreshAt) {
		return
	}
	// Only current, authenticated quality is eligible at commit. A historical
	// standby that did not pass preflight cannot become a data source.
	eligible := make([]int, 0, len(p.verified))
	for _, i := range p.verified {
		if p.states[i].ready && now.Before(p.states[i].validUntil) {
			eligible = append(eligible, i)
		}
	}
	sort.Slice(eligible, func(a, b int) bool { return cohortLess(p, eligible[a], eligible[b]) })
	old := append([]int(nil), p.good...)
	p.good = nil
	p.goodPos = map[int]int{}
	for _, i := range eligible {
		if len(p.good) >= p.policy.ActiveSize {
			break
		}
		p.addSelected(i)
		delete(c.standby, i)
	}
	for _, i := range old {
		c.observe(p, i)
	}
	c.generation++
	c.arm(p, now)
	for _, i := range p.good {
		if !p.states[i].pending {
			p.schedule(i, now)
		}
	}
	p.recomputeBest()
}
func (c *sourceCohort) maintenance(p *SourcePool, now time.Time) int {
	selected, reserve := -1, -1
	for _, i := range p.good {
		s := p.states[i]
		if !s.pending && !s.nextProbe.After(now) && (selected < 0 || s.nextProbe.Before(p.states[selected].nextProbe)) {
			selected = i
		}
	}
	for i := range c.standby {
		s := p.states[i]
		if c.needs(p, i) && !s.pending && !s.nextProbe.After(now) && (reserve < 0 || s.nextProbe.Before(p.states[reserve].nextProbe) || (s.nextProbe.Equal(p.states[reserve].nextProbe) && i < reserve)) {
			reserve = i
		}
	}
	c.turn++
	if reserve >= 0 && (selected < 0 || c.turn%2 == 0) {
		return reserve
	}
	return selected
}
func (c *sourceCohort) refill(p *SourcePool, now time.Time) {
	for len(p.good) < p.policy.ActiveSize {
		best := -1
		for _, i := range p.verified {
			if !p.isSelected(i) && p.states[i].ready && now.Before(p.states[i].validUntil) && (best < 0 || cohortLess(p, i, best)) {
				best = i
			}
		}
		if best < 0 {
			break
		}
		p.addSelected(best)
		delete(c.standby, best)
		if !p.states[best].pending {
			p.schedule(best, now)
		}
	}
	if len(p.good) < p.policy.ActiveSize {
		for i := range c.standby {
			if !p.states[i].pending && p.states[i].nextProbe.After(now) {
				p.schedule(i, now)
			}
		}
	}
	p.recomputeBest()
}
