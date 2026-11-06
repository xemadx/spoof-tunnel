package l3

import (
	"net"
	"testing"
	"time"
)

func TestCohortRestrictsDataAndRefreshesFromFreshQuality(t *testing.T) {
	for _, mode := range []string{"adaptive", "round_robin", "failover"} {
		t.Run(mode, func(t *testing.T) {
			ips := []net.IP{}
			for i := 1; i <= 12; i++ {
				ips = append(ips, net.IPv4(192, 0, 2, byte(i)))
			}
			p := NewSourcePool(ips, PoolPolicy{Mode: mode, ProbePPS: 25, ActiveSize: 2, RefreshSeconds: 60})
			start := time.Unix(100000, 0)
			for tick := 0; tick < 3200; tick++ {
				now := start.Add(time.Duration(tick) * 40 * time.Millisecond)
				p.Expire(now, true)
				token, ip := p.NextProbe(now)
				if token != "" {
					delay := time.Duration(20-int(ip[3])) * time.Millisecond
					if !p.Acknowledge(token, ip.String(), now.Add(delay)) {
						t.Fatal("valid correlated ACK rejected")
					}
				}
				if len(p.good) > 2 {
					t.Fatal("selected capacity exceeded")
				}
				if p.Ready() {
					source, e := p.Select(false)
					if e != nil || !p.isSelected(p.positions[source.String()]) {
						t.Fatal("data escaped selected cohort", source, e)
					}
				}
				if tick > 100 && tick < 1400 {
					for _, i := range p.good {
						if i > 1 {
							t.Fatal("discovery changed the full cohort before refresh")
						}
					}
				}
			}
			v := p.View(0, 50, "selected")
			if v.Generation != 3 || v.Selected != 2 || len(v.Items) != 2 {
				t.Fatalf("refresh/membership: %+v", v)
			}
			for _, row := range v.Items {
				if !row.Selected || row.LastSuccess < start.Add(110*time.Second).Unix() {
					t.Fatal("stale or non-selected member", row)
				}
			}
			// The best historical candidates are revalidated before admission.
			if !p.isSelected(11) || !p.isSelected(10) {
				t.Fatalf("best verified candidates not selected: %+v", v.Items)
			}
		})
	}
}
func TestCohortFailureDoesNotFillWithUnverifiedSources(t *testing.T) {
	p := NewSourcePool([]net.IP{net.ParseIP("192.0.2.1"), net.ParseIP("192.0.2.2")}, PoolPolicy{Mode: "adaptive", ActiveSize: 100})
	now := time.Unix(100000, 0)
	for tick := 0; tick < 100; tick++ {
		at := now.Add(time.Duration(tick) * 100 * time.Millisecond)
		token, ip := p.NextProbe(at)
		if token != "" && ip.String() == "192.0.2.1" {
			p.Acknowledge(token, ip.String(), at.Add(time.Millisecond))
		}
		p.Expire(at, true)
	}
	if got := p.View(0, 50, "").Selected; got != 1 {
		t.Fatalf("quota filled with unverified sources: %d", got)
	}
	p.Expire(now.Add(5*time.Minute), false)
	if p.Ready() {
		t.Fatal("expired cohort still allowed data")
	}
}
func TestCohortPolicyBudget(t *testing.T) {
	for _, p := range []PoolPolicy{{Mode: "legacy", ActiveSize: 1}, {Mode: "adaptive", ProbePPS: 1, ActiveSize: 100}, {Mode: "adaptive", ActiveSize: 401}, {Mode: "adaptive", ActiveSize: 1, RefreshSeconds: 59}} {
		if p.Validate() == nil {
			t.Fatal("accepted unsafe verification budget", p)
		}
	}
	if e := (PoolPolicy{Mode: "adaptive", ProbePPS: 25, ActiveSize: 100, RefreshSeconds: 1800}).Validate(); e != nil {
		t.Fatal(e)
	}
}
