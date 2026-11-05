package manager

import (
	"fmt"
	"strings"
	"testing"
)

func TestHistoryAndLiveLogBoundaryIsAtomic(t *testing.T) {
	for attempt := 0; attempt < 60; attempt++ {
		manager := NewManager(nil, "unused", t.TempDir())
		inst := manager.getInstance(1)
		var input strings.Builder
		for i := 0; i < 50; i++ {
			fmt.Fprintf(&input, "line-%d\n", i)
		}
		done := make(chan struct{})
		go func() { streamLogs(inst, strings.NewReader(input.String())); close(done) }()
		history, sub, ch := manager.FollowLogs(1, 100)
		<-done
		manager.UnsubscribeLogs(1, sub)
		all := append([]string(nil), history...)
		for line := range ch {
			all = append(all, line)
		}
		seen := map[string]bool{}
		for _, line := range all {
			if seen[line] {
				t.Fatal("duplicate at snapshot/live boundary", line)
			}
			seen[line] = true
		}
		if len(all) != 50 {
			t.Fatal("lost log line", len(all))
		}
	}
}
func TestSlowLogSubscriberClosesForResynchronization(t *testing.T) {
	manager := NewManager(nil, "unused", t.TempDir())
	inst := manager.getInstance(1)
	_, sub, ch := manager.FollowLogs(1, 100)
	var input strings.Builder
	for i := 0; i < 400; i++ {
		fmt.Fprintf(&input, "line-%d\n", i)
	}
	streamLogs(inst, strings.NewReader(input.String()))
	count := 0
	for range ch {
		count++
	}
	if count != 256 {
		t.Fatal("subscriber did not close on overflow", count)
	}
	manager.UnsubscribeLogs(1, sub)
	history, sub, _ := manager.FollowLogs(1, 100)
	defer manager.UnsubscribeLogs(1, sub)
	if len(history) != 100 || history[99] != "line-399" {
		t.Fatal("resynchronization lost newest history")
	}
}
