package l3

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"time"
)

type PeerPoolView struct {
	InFlight  bool   `json:"in_flight"`
	Supported bool   `json:"supported"`
	Status    string `json:"status"`
	Error     string `json:"error,omitempty"`
	Count     int    `json:"count"`
	Revision  string `json:"revision,omitempty"`
	Progress  int    `json:"progress"`
}
type PoolRuntimeView struct {
	PeerReady          bool         `json:"peer_ready"`
	PersistenceError   string       `json:"persistence_error,omitempty"`
	Pool               PoolView     `json:"pool"`
	Peer               PeerPoolView `json:"peer"`
	OurSourcesAccepted bool         `json:"our_sources_accepted"`
	ControlFresh       bool         `json:"control_fresh"`
}
type PoolControlRequest struct {
	Op     string `json:"op"`
	Offset int    `json:"offset"`
	Limit  int    `json:"limit"`
	Filter string `json:"filter"`
}
type PoolControlResponse struct {
	Status  *PoolRuntimeView `json:"status,omitempty"`
	Sources []string         `json:"sources,omitempty"`
	Error   string           `json:"error,omitempty"`
}

// PoolControl is local-only IPC; it never sends panel credentials over a network.
func PoolControl(socket string, req PoolControlRequest) (PoolControlResponse, error) {
	var out PoolControlResponse
	c, err := net.DialTimeout("unix", socket, 2*time.Second)
	if err != nil {
		return out, err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	if err = json.NewEncoder(c).Encode(req); err != nil {
		return out, err
	}
	err = json.NewDecoder(io.LimitReader(c, 8<<20)).Decode(&out)
	if err == nil && out.Error != "" {
		err = fmt.Errorf("%s", out.Error)
	}
	return out, err
}
