package api

import (
	"testing"
	"time"

	"github.com/ParsaKSH/spoof-tunnel/panel/internal/db"
)

// The peer wait scales with the manifest: a 122k-source pool needs about
// 5 minutes for the transfer alone, and the old fixed 100 seconds rolled
// back a healthy pair with the manifest only ~30% received.
func TestApplyPeerWaitSeconds(t *testing.T) {
	cases := []struct {
		name       string
		localTotal int
		peerCount  int
		pps        int
		want       time.Duration
	}{
		{"small pool keeps the 100s floor", 100, 100, 25, 100 * time.Second},
		{"empty pool keeps the floor", 0, 0, 25, 100 * time.Second},
		{"zero pps defaults to 25", 100, 100, 0, 100 * time.Second},
		{"122k sources at pps 25 waits about 7 minutes", 122437, 122617, 25, 427 * time.Second},
		{"larger peer side drives the wait", 100, 122617, 25, 427 * time.Second},
		{"faster probing shortens the wait", 122437, 122617, 50, 243 * time.Second},
		{"max pool stays under the 15m cap", 262144, 262144, 25, 846 * time.Second},
		{"oversized pool hits the 15m cap", 500000, 500000, 25, 900 * time.Second},
	}
	for _, tc := range cases {
		if got := applyPeerWaitSeconds(tc.localTotal, tc.peerCount, tc.pps); got != tc.want {
			t.Errorf("%s: got %s, want %s", tc.name, got, tc.want)
		}
	}
}

func TestPoolSourceCounts(t *testing.T) {
	inst := db.TunnelInstance{
		SpoofIPList:     "1.1.1.1\n2.2.2.2\n",
		PeerSpoofIPList: "3.3.3.3\n",
	}
	localTotal, peerCount := poolSourceCounts(inst)
	if localTotal != 2 || peerCount != 1 {
		t.Errorf("got (%d, %d), want (2, 1)", localTotal, peerCount)
	}
	localTotal, peerCount = poolSourceCounts(db.TunnelInstance{})
	if localTotal != 0 || peerCount != 0 {
		t.Errorf("empty instance: got (%d, %d), want (0, 0)", localTotal, peerCount)
	}
}
