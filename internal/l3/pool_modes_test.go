package l3

import (
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"
)

func confirmedPool(t *testing.T, mode string) (*SourcePool, time.Time) {
	t.Helper()
	p := NewSourcePool([]net.IP{net.ParseIP("192.0.2.1"), net.ParseIP("192.0.2.2"), net.ParseIP("192.0.2.3")}, PoolPolicy{Mode: mode})
	now := time.Unix(100000, 0)
	for i := 0; i < 30 && p.View(0, 1, "").Healthy < 3; i++ {
		if token, ip := p.NextProbe(now); token != "" {
			rtt := 10 * time.Millisecond
			if ip.String() == "192.0.2.1" {
				rtt = 40 * time.Millisecond
			}
			if !p.Acknowledge(token, ip.String(), now.Add(rtt)) {
				t.Fatal("bootstrap acknowledgement failed")
			}
		}
		now = now.Add(time.Second)
	}
	if p.View(0, 1, "").Healthy != 3 {
		t.Fatal("not all candidates verified")
	}
	return p, now
}
func TestAllSmartModesSelectionFailureAndRestartRecovery(t *testing.T) {
	for _, mode := range []string{"adaptive", "round_robin", "failover"} {
		t.Run(mode, func(t *testing.T) {
			p, now := confirmedPool(t, mode)
			counts := map[string]int{}
			for i := 0; i < 30; i++ {
				p.Select(true)
				ip, err := p.Select(false)
				if err != nil {
					t.Fatal(err)
				}
				counts[ip.String()]++
			}
			if mode == "adaptive" && counts["192.0.2.2"] != 30 {
				t.Fatal("adaptive did not choose better latency", counts)
			}
			if mode == "failover" && counts["192.0.2.1"] != 30 {
				t.Fatal("failover ignored configured priority", counts)
			}
			if mode == "round_robin" && (counts["192.0.2.1"] != 10 || counts["192.0.2.2"] != 10 || counts["192.0.2.3"] != 10) {
				t.Fatal("healthy rotation skewed", counts)
			}
			bad := p.View(0, 1, "").Active
			var deadline int64
			for i := 0; i < 240; i++ {
				now = now.Add(250 * time.Millisecond)
				p.Expire(now, true)
				if token, ip := p.NextProbe(now); token != "" && ip.String() != bad {
					p.Acknowledge(token, ip.String(), now.Add(time.Millisecond))
				}
				for _, row := range p.View(0, 10, "").Items {
					if row.IP == bad && row.State == "quarantined" {
						deadline = row.RetryAt
						if row.Reason == "" {
							t.Fatal("quarantine has no diagnostic reason")
						}
					}
				}
				if deadline > 0 {
					break
				}
				p.Select(false)
			}
			if deadline == 0 {
				t.Fatal("failed active source never quarantined")
			}
			if remaining := deadline - now.Unix(); remaining < 600 || remaining > 601 {
				t.Fatal("wrong cooldown", remaining)
			}
			for i := 0; i < 30; i++ {
				ip, err := p.Select(false)
				if err != nil || ip.String() == bad {
					t.Fatal("failed source remained in data", ip, err)
				}
			}
			if p.View(0, 1, "").Active == bad {
				t.Fatal("telemetry displayed quarantined source as active")
			}
			restored := NewSourcePool(p.ips, p.policy)
			restored.Restore(p.Checkpoint(), now)
			if restored.Ready() {
				t.Fatal("restart promoted stale quality")
			}
			before := now.Add(600*time.Second - time.Millisecond)
			for i := 0; i < 3; i++ {
				_, ip := restored.NextProbe(before)
				if ip != nil && ip.String() == bad {
					t.Fatal("quarantine ended early after restart")
				}
			}
			now = time.Unix(deadline, 0)
			restored.Expire(now, false)
			replies := 0
			for i := 0; i < 30; i++ {
				token, ip := restored.NextProbe(now)
				if token != "" {
					if ip.String() == bad {
						replies++
						if replies < 2 && restored.states[restored.positions[bad]].ready {
							t.Fatal("retest promoted before confirmation")
						}
					}
					restored.Acknowledge(token, ip.String(), now.Add(time.Millisecond))
				}
				if restored.states[restored.positions[bad]].ready {
					break
				}
				now = now.Add(time.Second)
				restored.Expire(now, false)
			}
			if replies < 2 || !restored.states[restored.positions[bad]].ready {
				t.Fatal("expired source failed re-verification")
			}
		})
	}
}
func TestEveryModeDoesNotBlameAllSourcesForReverseOutage(t *testing.T) {
	for _, mode := range []string{"adaptive", "round_robin", "failover"} {
		t.Run(mode, func(t *testing.T) {
			p, now := confirmedPool(t, mode)
			p.Select(false)
			for i := 0; i < 300; i++ {
				now = now.Add(time.Second)
				p.NextProbe(now)
				p.Expire(now, false)
			}
			view := p.View(-20, 10, "")
			if view.Quarantined != 0 || view.Healthy != 0 || view.Active != "" || view.Offset != 0 {
				t.Fatal("outage/stale telemetry wrong", view)
			}
			for _, row := range view.Items {
				if row.Failures != 0 {
					t.Fatal("reverse outage blamed candidate", row)
				}
			}
			if _, err := p.Select(false); err == nil {
				t.Fatal("stale quality carried data")
			}
		})
	}
}
func TestPoolEventIsBoundedAndDoesNotRequireOptionalManifest(t *testing.T) {
	v := PoolRuntimeView{Pool: PoolView{Mode: "adaptive", Healthy: 1, Total: 122437}, PeerReady: true, ControlFresh: true}
	var event map[string]any
	if err := json.Unmarshal([]byte(PoolEvent(v)), &event); err != nil || event["reason"] != "ready" {
		t.Fatal("manual allowlist falsely shown waiting", event, err)
	}
	v.AutoPeerSync = true
	if !strings.Contains(PoolEvent(v), "awaiting_peer_manifest") || len(PoolEvent(v)) > 1500 {
		t.Fatal("invalid event")
	}
}
