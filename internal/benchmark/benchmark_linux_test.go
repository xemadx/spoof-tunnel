//go:build linux

package benchmark

import (
	"context"
	"encoding/json"
	"github.com/ParsaKSH/spoof-tunnel/internal/l3"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

type lossyBenchmarkWire struct {
	Wire
	packets atomic.Int64
}

func (w *lossyBenchmarkWire) Send(b []byte) error {
	if len(b) > 1100 {
		n := w.packets.Add(1)
		if n%10 == 0 {
			return nil
		}
		if n%7 == 0 {
			_ = w.Wire.Send(b)
		}
	}
	return w.Wire.Send(b)
}
func TestDeliveredGoodputIgnoresSuccessfulWritesAndDuplicates(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("raw sockets require root")
	}
	a := NewRunner(func(c l3.Config, tag []byte) (Wire, error) {
		w, e := RawFactory(c, tag)
		if e != nil {
			return nil, e
		}
		return &lossyBenchmarkWire{Wire: w}, nil
	})
	b := NewRunner(RawFactory)
	defer a.Stop()
	defer b.Stop()
	code, e := b.Receiver(Invitation{A: "127.0.0.95", B: "127.0.0.96", SourceA: "127.1.95.1", SourceB: "127.1.96.1", PortA: 39695, PortB: 39696}, nil)
	if e != nil {
		t.Fatal(e)
	}
	inv, e := Decode(code)
	if e != nil {
		t.Fatal(e)
	}
	s, e := a.start(inv, "local", Options{}, nil)
	if e != nil {
		t.Fatal(e)
	}
	o := Options{RateMbps: 1, Seconds: 2}
	s.current = newTrial(0, o, "icmp", "icmp")
	if _, e = s.rpc(s.ctx, "icmp", "icmp", "prepare", 0, o, 4); e != nil {
		t.Fatal(e)
	}
	d := s.emit(s.current, 0, "icmp")
	time.Sleep(200 * time.Millisecond)
	body, e := s.rpc(s.ctx, "icmp", "icmp", "upload_report", 0, nil, 4)
	if e != nil {
		t.Fatal(e)
	}
	var received int
	if json.Unmarshal(body, &received) != nil {
		t.Fatal("invalid receipt report")
	}
	want := d.Sent - d.Sent/10
	if received != want {
		t.Fatalf("receipt count=%d want=%d writes=%d", received, want, d.Sent)
	}
	measured := measure(d, received, o.RateMbps)
	if measured.LossPct < 9 || measured.LossPct > 11 || measured.Capped {
		t.Fatalf("loss or goodput flattered by writes or duplicates: %+v", measured)
	}
}

func TestRawAll25CarrierPairs(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("raw-socket integration requires root")
	}
	a, b := NewRunner(RawFactory), NewRunner(RawFactory)
	defer a.Stop()
	defer b.Stop()
	code, e := b.Receiver(Invitation{A: "127.0.0.81", B: "127.0.0.82", SourceA: "127.1.81.1", SourceB: "127.1.82.1", PortA: 39881, PortB: 39882}, nil)
	if e != nil {
		t.Fatal(e)
	}
	if e = a.Controller(code, Options{RateMbps: 1, Seconds: 2}, nil); e != nil {
		t.Fatal(e)
	}
	deadline := time.After(4 * time.Minute)
	for {
		select {
		case <-deadline:
			t.Fatalf("timeout: %+v", a.Status())
		case <-time.After(250 * time.Millisecond):
			v := a.Status()
			if v.Status == "done" {
				if len(v.Results) != 25 || v.Recommended < 0 {
					t.Fatalf("incomplete benchmark: %+v", v)
				}
				for _, r := range v.Results {
					if r.Status != "passed" || r.Upload.Received == 0 || r.Download.Received == 0 || r.Upload.Received > r.Upload.Sent || r.Download.Received > r.Download.Sent || r.PingReplies < 8 {
						t.Errorf("bad measured carrier result: %+v", r)
					}
				}
				if b.Status().Recommended < 0 {
					t.Error("receiver did not retain successful result")
				}
				return
			}
		}
	}
}
func TestBenchmarkEscalationMeasuresAboveBaseRate(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("raw-socket integration requires root")
	}
	a, b := NewRunner(RawFactory), NewRunner(RawFactory)
	defer a.Stop()
	defer b.Stop()
	code, e := b.Receiver(Invitation{A: "127.0.0.83", B: "127.0.0.84", SourceA: "127.1.83.1", SourceB: "127.1.84.1", PortA: 39883, PortB: 39884}, nil)
	if e != nil {
		t.Fatal(e)
	}
	if e = a.Controller(code, Options{RateMbps: 1, Seconds: 2, Escalate: true, MaxRateMbps: 2}, nil); e != nil {
		t.Fatal(e)
	}
	deadline := time.After(6 * time.Minute)
	for {
		select {
		case <-deadline:
			t.Fatalf("timeout: %+v", a.Status())
		case <-time.After(250 * time.Millisecond):
			v := a.Status()
			if v.Status == "done" {
				if len(v.Results) != 25 || v.Recommended < 0 {
					t.Fatalf("incomplete benchmark: %+v", v)
				}
				for _, r := range v.Results {
					if r.Status != "passed" {
						t.Fatalf("cell %d did not pass: %+v", r.Index, r)
					}
					// Loopback is far faster than 1 Mbps, so every cell
					// must have escalated to the 2 Mbps ceiling, and the
					// reported goodput must come from that round.
					if !r.Escalated || r.RateMbps != 2 {
						t.Fatalf("cell %d was not re-measured at the ceiling: %+v", r.Index, r)
					}
					if r.Upload.Mbps <= 1 || r.Download.Mbps <= 1 {
						t.Fatalf("cell %d reports base-rate goodput after escalation: %+v", r.Index, r)
					}
					if !r.Upload.Capped || !r.Download.Capped {
						t.Fatalf("cell %d should still be capped at the 2 Mbps ceiling on loopback: %+v", r.Index, r)
					}
				}
				return
			}
		}
	}
}
func TestOptionsEscalationValidation(t *testing.T) {
	if e := (Options{RateMbps: 20, Seconds: 3, Escalate: true, MaxRateMbps: 100}).Validate(); e != nil {
		t.Fatal(e)
	}
	if e := (Options{RateMbps: 20, Seconds: 3, Escalate: true}).Validate(); e != nil {
		t.Fatal(e)
	}
	if (Options{RateMbps: 20, Seconds: 3, Escalate: true}).maxRate() != 200 {
		t.Fatal("default escalation ceiling is not 200 Mbps")
	}
	for _, bad := range []Options{
		{RateMbps: 20, Seconds: 3, MaxRateMbps: 10},
		{RateMbps: 20, Seconds: 3, MaxRateMbps: 501},
		{RateMbps: 0, Seconds: 3},
		{RateMbps: 501, Seconds: 3},
		{RateMbps: 20, Seconds: 9},
	} {
		if bad.Validate() == nil {
			t.Fatalf("invalid options accepted: %+v", bad)
		}
	}
	// The escalation round itself measures at the ceiling rate.
	if e := (Options{RateMbps: 100, Seconds: 3}).Validate(); e != nil {
		t.Fatalf("escalation round options rejected: %v", e)
	}
}
func TestCountersAndMeasuredRates(t *testing.T) {
	c := counter{bits: make([]uint64, 2)}
	for _, n := range []int{0, 0, 1, 63, 64, 64, 127, -1, 128} {
		c.add(n)
	}
	if c.count != 5 {
		t.Fatal("duplicates or invalid packet sequences counted", c.count)
	}
	d := measure(Direction{Sent: 100, Seconds: 2, SendErrors: 2}, 80, 1)
	if d.LossPct != 20 || d.Mbps != .352 || d.Received != 80 || d.Capped {
		t.Fatalf("incorrect delivered goodput/loss: %+v", d)
	}
	if Recommend([]Result{{Index: 0, Status: "failed", Score: 999}, {Index: 1, Status: "passed", Score: 100, PingReplies: 7, Upload: Direction{Received: 1}, Download: Direction{Received: 1}}, {Index: 2, Status: "passed", Score: 2, PingReplies: 12, Upload: Direction{Received: 10}, Download: Direction{Received: 10}}}) != 2 {
		t.Fatal("unqualified trial won")
	}
}
func TestUnauthenticatedOrUnmeasuredApplyIsRejected(t *testing.T) {
	s := &Session{view: Snapshot{Results: []Result{}}, hook: func(context.Context, string, json.RawMessage) (any, error) {
		t.Fatal("unmeasured apply reached hook")
		return nil, nil
	}}
	if _, e := s.handle(message{Op: "apply_prepare", Trial: 0, Return: "icmp"}, "icmp"); e == nil {
		t.Fatal("unmeasured plan accepted")
	}
	if _, e := Decode("garbage"); e == nil {
		t.Fatal("invalid capability accepted")
	}
}
