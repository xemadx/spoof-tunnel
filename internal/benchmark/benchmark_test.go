package benchmark

import (
	"encoding/json"
	"testing"
)

// The escalation round re-prepares the same cell at a higher rate. The
// receiver must rebuild its trial with the new options, or it would keep
// emitting at the base rate while the controller measures at the ceiling.
func TestReprepareRebuildsTrialAtNewRate(t *testing.T) {
	s := &Session{view: Snapshot{Results: []Result{}}}
	prepare := func(rate int) {
		t.Helper()
		body, err := json.Marshal(Options{RateMbps: rate, Seconds: 3})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.handle(message{Op: "prepare", Trial: 0, Return: "icmp", Body: body}, "icmp"); err != nil {
			t.Fatalf("prepare at %d Mbps failed: %v", rate, err)
		}
	}
	prepare(20)
	if s.current == nil || s.current.options.RateMbps != 20 {
		t.Fatalf("base trial not created at 20 Mbps: %+v", s.current)
	}
	prepare(100)
	if s.current == nil || s.current.options.RateMbps != 100 {
		t.Fatalf("re-prepare kept the old rate: %+v", s.current.options)
	}
	// A re-prepare at the same rate must not disturb the trial either.
	prepare(100)
	if s.current == nil || s.current.options.RateMbps != 100 {
		t.Fatalf("same-rate re-prepare broke the trial: %+v", s.current.options)
	}
}
