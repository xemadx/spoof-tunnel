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

// The ladder doubles the offered rate with no user-chosen maximum until
// the measurement ceiling, and the capacity of a cell is its weaker
// direction.
func TestLadderNextRate(t *testing.T) {
	rungs := []int{}
	for next := ladderNextRate(20, 500); next != 0; next = ladderNextRate(next, 500) {
		rungs = append(rungs, next)
	}
	want := []int{40, 80, 160, 320, 500}
	if len(rungs) != len(want) {
		t.Fatalf("ladder rungs from 20 Mbps: %v, want %v", rungs, want)
	}
	for i, w := range want {
		if rungs[i] != w {
			t.Fatalf("ladder rung %d: %d, want %d", i, rungs[i], w)
		}
	}
	if ladderNextRate(500, 500) != 0 {
		t.Fatal("ladder did not stop at the ceiling")
	}
	if ladderNextRate(400, 500) != 500 {
		t.Fatal("last rung did not land exactly on the ceiling")
	}
	if got := minDirectionMbps(Result{Upload: Direction{Mbps: 42}, Download: Direction{Mbps: 17}}); got != 17 {
		t.Fatalf("weaker direction: %v, want 17", got)
	}
}
