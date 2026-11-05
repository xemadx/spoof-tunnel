package l3

import "encoding/json"

// PoolEvent deliberately excludes keys, tokens and complete source inventories.
// The runtime coalesces these bounded summaries rather than logging every probe.
func PoolEvent(v PoolRuntimeView) string {
	reason := "ready"
	switch {
	case v.Pool.Healthy == 0:
		reason = "no_verified_source"
	case v.AutoPeerSync && !v.OurSourcesAccepted:
		reason = "awaiting_peer_manifest"
	case !v.PeerReady:
		reason = "awaiting_peer_readiness"
	case !v.ControlFresh:
		reason = "reverse_reference_stale"
	}
	event := struct {
		Event            string `json:"event"`
		Mode             string `json:"mode"`
		Total            int    `json:"total"`
		Healthy          int    `json:"healthy"`
		Testing          int    `json:"testing"`
		Unknown          int    `json:"unknown"`
		Quarantined      int    `json:"quarantined"`
		Active           string `json:"active,omitempty"`
		PeerReady        bool   `json:"peer_ready"`
		ManifestAccepted bool   `json:"manifest_accepted"`
		ControlFresh     bool   `json:"control_fresh"`
		PeerStatus       string `json:"peer_status"`
		PeerCount        int    `json:"peer_count"`
		PeerProgress     int    `json:"peer_progress"`
		Reason           string `json:"reason"`
		Detail           string `json:"detail,omitempty"`
	}{"spoof_pool", v.Pool.Mode, v.Pool.Total, v.Pool.Healthy, v.Pool.Testing, v.Pool.Unknown, v.Pool.Quarantined, v.Pool.Active, v.PeerReady, v.OurSourcesAccepted, v.ControlFresh, v.Peer.Status, v.Peer.Count, v.Peer.Progress, reason, v.Peer.Error}
	b, _ := json.Marshal(event)
	return string(b)
}
