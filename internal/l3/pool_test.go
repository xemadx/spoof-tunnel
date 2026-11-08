package l3

import (
	"bytes"
	"fmt"
	"net"
	"testing"
	"time"
)

func testPool() (*SourcePool, time.Time) {
	return NewSourcePool([]net.IP{net.ParseIP("192.0.2.1")}, PoolPolicy{Mode: "adaptive"}), time.Unix(100000, 0)
}
func TestSeedVerifiedPreMarksProvenSource(t *testing.T) {
	ips := []net.IP{net.ParseIP("192.0.2.1"), net.ParseIP("192.0.2.2")}
	p := NewSourcePool(ips, PoolPolicy{Mode: "adaptive"})
	if p.Ready() {
		t.Fatal("pool ready before any verification")
	}
	if _, err := p.Select(false); err == nil {
		t.Fatal("data selection allowed before verification")
	}
	if !p.SeedVerified(net.ParseIP("192.0.2.2"), 2*time.Minute) {
		t.Fatal("seed rejected for a pool member")
	}
	if !p.Ready() {
		t.Fatal("pool not ready after seed")
	}
	ip, err := p.Select(false)
	if err != nil || ip.String() != "192.0.2.2" {
		t.Fatalf("data selection did not pick the seeded source: %v %v", ip, err)
	}
	if p.SeedVerified(net.ParseIP("192.0.2.3"), time.Minute) {
		t.Fatal("seed accepted for an address outside the pool")
	}
	// A quarantined source must not be seeded back to healthy.
	p2 := NewSourcePool(ips, PoolPolicy{Mode: "adaptive"})
	p2.Restore(map[string]int64{"192.0.2.1": time.Now().Add(time.Hour).Unix()}, time.Now())
	if p2.SeedVerified(net.ParseIP("192.0.2.1"), time.Minute) {
		t.Fatal("seed revived a quarantined source")
	}
}

func TestPoolAuthenticatedAttributionAndQuarantine(t *testing.T) {
	p, now := testPool()
	token, ip := p.NextProbe(now)
	if p.Acknowledge("wrong", ip.String(), now.Add(time.Millisecond)) || p.Acknowledge(token, "192.0.2.2", now.Add(time.Millisecond)) {
		t.Fatal("accepted an uncorrelated acknowledgement")
	}
	if !p.Acknowledge(token, ip.String(), now.Add(20*time.Millisecond)) {
		t.Fatal("valid acknowledgement failed")
	}
	now = now.Add(time.Second)
	token, ip = p.NextProbe(now)
	if !p.Acknowledge(token, ip.String(), now.Add(10*time.Millisecond)) {
		t.Fatal("confirmation failed")
	}
	if _, err := p.Select(false); err != nil {
		t.Fatal(err)
	}
	now = now.Add(11 * time.Second)
	for i := 0; i < 3; i++ {
		token, _ = p.NextProbe(now)
		if token == "" {
			t.Fatal("missing scheduled probe")
		}
		now = now.Add(4 * time.Second)
		p.Expire(now, true)
		now = now.Add(time.Second)
	}
	v := p.View(0, 50, "")
	if v.Quarantined != 1 || v.Healthy != 0 {
		t.Fatalf("not quarantined: %+v", v)
	}
	if _, err := p.Select(false); err == nil {
		t.Fatal("quarantined address carried data")
	}
	until := time.Unix(v.Items[0].RetryAt, 0)
	if token, _ = p.NextProbe(until.Add(-time.Millisecond)); token != "" {
		t.Fatal("quarantine ended early")
	}
	token, ip = p.NextProbe(until)
	if token == "" {
		t.Fatal("expired quarantine was not retested")
	}
	if _, err := p.Select(false); err == nil {
		t.Fatal("expiry promoted unverified address")
	}
	if !p.Acknowledge(token, ip.String(), until.Add(time.Millisecond)) {
		t.Fatal("retest failed")
	}
	token, ip = p.NextProbe(until.Add(time.Second))
	p.Acknowledge(token, ip.String(), until.Add(time.Second+time.Millisecond))
	if _, err := p.Select(false); err != nil {
		t.Fatal("recovered address not admitted", err)
	}
}
func TestPoolReverseOutageDoesNotBlacklistEverything(t *testing.T) {
	p, now := testPool()
	for i := 0; i < 20; i++ {
		token, _ := p.NextProbe(now)
		if token == "" {
			t.Fatal("no probe")
		}
		now = now.Add(16 * time.Second)
		p.Expire(now, false)
		now = now.Add(6 * time.Second)
	}
	v := p.View(0, 50, "")
	if v.Quarantined != 0 || v.Items[0].Failures != 0 {
		t.Fatalf("reverse outage blamed source: %+v", v)
	}
}
func TestPoolCheckpointPreservesDeadline(t *testing.T) {
	p, now := testPool()
	p.Restore(map[string]int64{"192.0.2.1": now.Add(500 * time.Second).Unix()}, now)
	if token, _ := p.NextProbe(now); token != "" {
		t.Fatal("restored quarantine ignored")
	}
	if p.Checkpoint()["192.0.2.1"] != now.Add(500*time.Second).Unix() {
		t.Fatal("deadline changed")
	}
	if token, _ := p.NextProbe(now.Add(500 * time.Second)); token == "" {
		t.Fatal("restored quarantine did not expire")
	}
}
func TestPoolLargeFileAndManifest(t *testing.T) {
	var text bytes.Buffer
	for i := 0; i < 122437; i++ {
		ip := net.IPv4(10, byte(i>>16), byte(i>>8), byte(i))
		text.WriteString(ip.String() + "\n")
	}
	ips, err := ParseSourceText(&text)
	if err != nil {
		t.Fatal(err)
	}
	data, digest, err := packSources(ips)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := unpackSources(data, digest)
	if err != nil || len(decoded) != len(ips) {
		t.Fatal("large manifest round trip failed", err)
	}
	if _, err = unpackSources(data, "invalid"); err == nil {
		t.Fatal("corrupt digest accepted")
	}
	p := NewSourcePool(ips, PoolPolicy{Mode: "round_robin"})
	if v := p.View(120000, 50, ""); v.Total != 122437 || len(v.Items) != 50 {
		t.Fatal("large pool was not paginated")
	}
}
func TestSourceTextRejectsAmbiguousRanges(t *testing.T) {
	for _, s := range []string{"192.0.2.0/24", "1.1.1.1-1.1.1.2", "0.0.0.0", "255.255.255.255", "not-an-ip"} {
		if _, err := ParseSourceText(bytes.NewBufferString(s)); err == nil {
			t.Fatal("accepted", s)
		}
	}
	ips, err := ParseSourceText(bytes.NewBufferString("\ufeff192.0.2.1\r\n192.0.2.1\n# comment\n"))
	if err != nil || len(ips) != 1 {
		t.Fatal("BOM/CRLF/dedup failed", err)
	}
}

func TestPoolLargePacketFailureIsNotHiddenBySmallSuccess(t *testing.T) {
	p, now := testPool()
	for i := 0; i < 12; i++ {
		token, ip := p.NextProbe(now)
		if token == "" {
			if p.View(0, 1, "").Quarantined == 1 {
				break
			}
			t.Fatal("missing probe")
		}
		if p.ProbeSize(token, 1280) > 64 {
			now = now.Add(4 * time.Second)
			p.Expire(now, true)
		} else {
			p.Acknowledge(token, ip.String(), now.Add(time.Millisecond))
		}
		now = now.Add(time.Second)
	}
	if p.View(0, 1, "").Quarantined != 1 {
		t.Fatal("MTU black hole never quarantined")
	}
	if _, err := p.Select(true); err == nil {
		t.Fatal("quarantined address still used for routine control")
	}
}
func TestHealthyRoundRobinDoesNotLoseAddressesToControlTraffic(t *testing.T) {
	p := NewSourcePool([]net.IP{net.ParseIP("192.0.2.1"), net.ParseIP("192.0.2.2")}, PoolPolicy{Mode: "round_robin"})
	now := time.Unix(100000, 0)
	for n := 0; n < 20 && p.View(0, 1, "").Healthy < 2; n++ {
		token, ip := p.NextProbe(now)
		if token != "" {
			p.Acknowledge(token, ip.String(), now.Add(time.Millisecond))
		}
		now = now.Add(time.Second)
	}
	if p.View(0, 1, "").Healthy != 2 {
		t.Fatal("setup did not confirm two sources")
	}
	counts := map[string]int{}
	for i := 0; i < 20; i++ {
		p.Select(true)
		ip, err := p.Select(false)
		if err != nil {
			t.Fatal(err)
		}
		counts[ip.String()]++
	}
	if counts["192.0.2.1"] != 10 || counts["192.0.2.2"] != 10 {
		t.Fatalf("control traffic skewed data rotation: %v", counts)
	}
}

func TestPoolSlowAuthenticatedPathAndRisingLatency(t *testing.T) {
	p, now := testPool()
	token, ip := p.NextProbe(now)
	p.Expire(now.Add(4*time.Second), true)
	if !p.Acknowledge(token, ip.String(), now.Add(6*time.Second)) {
		t.Fatal("slow initial path rejected")
	}
	now = now.Add(7 * time.Second)
	token, ip = p.NextProbe(now)
	p.ReferenceRTT(8 * time.Second)
	p.Expire(now.Add(10*time.Second), true)
	if !p.Acknowledge(token, ip.String(), now.Add(12*time.Second)) {
		t.Fatal("rising latency rejected")
	}
	if !p.Ready() || p.View(0, 1, "").Quarantined != 0 {
		t.Fatal("slow path not usable")
	}
}

func TestPoolActiveSourceBypassesLargeDiscoveryBacklog(t *testing.T) {
	ips := make([]net.IP, 3000)
	for i := range ips {
		ips[i] = net.IPv4(192, 0, byte(i>>8), byte(i))
	}
	p := NewSourcePool(ips, PoolPolicy{Mode: "adaptive"})
	now := time.Unix(100000, 0)
	// Reproduce a verified last source behind thousands of due retests.
	for i := range ips {
		p.schedule(i, now)
	}
	active := len(ips) - 1
	p.states[active].ready = true
	p.states[active].State = "healthy"
	p.states[active].validUntil = now.Add(time.Minute)
	p.good = []int{active}
	p.verified = []int{active}
	p.verifiedPos[active] = 0
	p.goodPos[active] = 0
	p.best = active
	p.active = active
	for i := 0; i < 126; i++ {
		p.pending[fmt.Sprint(i)] = sourceProbe{index: i, sent: now, deadline: now.Add(time.Minute)}
	}
	token, ip := p.NextProbe(now)
	if token == "" || !ip.Equal(ips[active]) {
		t.Fatal("active source starved by discovery")
	}
	p.Expire(now.Add(61*time.Second), false)
	if p.Ready() || p.View(0, 1, "").Healthy != 0 {
		t.Fatal("stale measurements still promoted data")
	}
}

func TestUnboundedPoolMaintainsAllHealthySourcesAheadOfDiscovery(t *testing.T) {
	ips := make([]net.IP, 1000)
	for i := range ips {
		ips[i] = net.IPv4(192, 0, byte(i>>8), byte(i))
	}
	p := NewSourcePool(ips, PoolPolicy{Mode: "adaptive"})
	now := time.Unix(100000, 0)
	// Set up 3 healthy sources in good (index 0, 1, 2)
	for i := 0; i < 3; i++ {
		p.states[i].ready = true
		p.states[i].State = "healthy"
		p.states[i].validUntil = now.Add(time.Minute)
		p.states[i].nextProbe = now
		p.good = append(p.good, i)
		p.goodPos[i] = i
		p.verified = append(p.verified, i)
		p.verifiedPos[i] = i
	}
	p.best = 0
	p.active = 0

	// NextProbe should probe candidate 0 (active/best)
	token1, ip1 := p.NextProbe(now)
	if token1 == "" || !ip1.Equal(ips[0]) {
		t.Fatalf("expected active candidate 0, got %v", ip1)
	}

	// NextProbe should probe candidate 1 (next ready source in good)
	token2, ip2 := p.NextProbe(now)
	if token2 == "" || !ip2.Equal(ips[1]) {
		t.Fatalf("expected good candidate 1, got %v", ip2)
	}

	// NextProbe should probe candidate 2 (next ready source in good)
	token3, ip3 := p.NextProbe(now)
	if token3 == "" || !ip3.Equal(ips[2]) {
		t.Fatalf("expected good candidate 2, got %v", ip3)
	}
}

func TestUnboundedPoolMaintenanceIntervalScales(t *testing.T) {
	ips := make([]net.IP, 500)
	for i := range ips {
		ips[i] = net.IPv4(192, 0, byte(i>>8), byte(i))
	}
	p := NewSourcePool(ips, PoolPolicy{Mode: "adaptive", ActiveSize: 0, ProbePPS: 25})
	for i := 0; i < 200; i++ {
		p.good = append(p.good, i)
		p.goodPos[i] = i
	}

	interval := p.maintenanceInterval()
	// To maintain 200 sources at 25 PPS without consuming more than 25% of PPS budget,
	// interval must be at least 4 * 200 / 25 = 32 seconds.
	expectedMin := 32 * time.Second
	if interval < expectedMin {
		t.Fatalf("unbounded pool maintenance interval did not scale with len(good): got %v, expected >= %v", interval, expectedMin)
	}
	lifetime := p.qualityLifetime(0)
	if lifetime < 3*expectedMin {
		t.Fatalf("unbounded pool quality lifetime did not scale with maintenance interval: got %v, expected >= %v", lifetime, 3*expectedMin)
	}
}
