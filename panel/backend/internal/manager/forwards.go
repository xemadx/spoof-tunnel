package manager

import (
	"fmt"
	"log"
	"net"

	"github.com/ParsaKSH/spoof-tunnel/panel/internal/db"
	"github.com/ParsaKSH/spoof-tunnel/panel/internal/forward"
)

// ForwardStatus is one rule plus its live forwarder state, for the panel.
type ForwardStatus struct {
	Rule      db.ForwardRule `json:"rule"`
	Target    string         `json:"target"` // peer TUN IP:target_port
	Listening bool           `json:"listening"`
	Stats     forward.Stats  `json:"stats"`
}

func targetAddr(inst db.TunnelInstance, rule db.ForwardRule) string {
	return net.JoinHostPort(inst.TunPeer, fmt.Sprint(rule.TargetPort))
}

// StartForwarders starts every enabled rule of an instance. A rule that
// fails to bind (port already in use, etc.) is kept in the map with its
// LastError so the UI can show it, but it never fails the tunnel start:
// the tunnel itself is still usable, and the user can fix the port and
// restart the tunnel.
func (m *Manager) StartForwarders(id uint) {
	ins := m.getInstance(id)
	ins.mu.Lock()
	defer ins.mu.Unlock()
	if ins.status != StatusRunning || ins.cmd == nil {
		return
	}
	var inst db.TunnelInstance
	if err := m.db.First(&inst, id).Error; err != nil {
		return
	}
	if inst.Engine != "tun" {
		return // forward-over-TUN only; legacy relay has its own path
	}
	var rules []db.ForwardRule
	if err := m.db.Where("instance_id = ? AND enabled = ?", id, true).Find(&rules).Error; err != nil {
		return
	}
	for _, rule := range rules {
		if err := startForwarder(ins, inst, rule); err != nil {
			log.Printf("[manager] forward rule %d failed to start: %v", rule.ID, err)
		}
	}
}

// StartForwarder starts (or restarts) a single rule. The manager lock
// is held across Stop(old) + Start(new) so two concurrent starts for
// the same rule cannot leave two listeners, with the second bind
// failing spuriously on the first one's port.
func (m *Manager) StartForwarder(inst db.TunnelInstance, rule db.ForwardRule) error {
	ins := m.getInstance(inst.ID)
	ins.mu.Lock()
	defer ins.mu.Unlock()
	if ins.status != StatusRunning || ins.cmd == nil {
		return fmt.Errorf("tunnel is not running")
	}
	return startForwarder(ins, inst, rule)
}

func startForwarder(ins *Instance, inst db.TunnelInstance, rule db.ForwardRule) error {
	ins.fwdMu.Lock()
	defer ins.fwdMu.Unlock()
	if old, ok := ins.forwarders[rule.ID]; ok {
		delete(ins.forwarders, rule.ID)
		// Stop outside the new forwarder's creation is safe: old's own
		// mutexes are independent of fwdMu.
		old.Stop()
	}
	fwd := forward.New(fmt.Sprintf("0.0.0.0:%d", rule.ListenPort), targetAddr(inst, rule))
	fwd.BindInterface = inst.TunInterface
	fwd.IsReady = func() bool {
		return ins.forwarderReady.Load()
	}
	ins.forwarders[rule.ID] = fwd
	return fwd.Start()
}

// StopForwarder stops one rule, if running.
func (m *Manager) StopForwarder(id, ruleID uint) {
	ins := m.getInstance(id)
	ins.fwdMu.Lock()
	fwd, ok := ins.forwarders[ruleID]
	if ok {
		delete(ins.forwarders, ruleID)
	}
	ins.fwdMu.Unlock()
	if ok {
		fwd.Stop()
	}
}

// StopForwarders stops every forwarder of an instance.
func (m *Manager) StopForwarders(id uint) {
	ins := m.getInstance(id)
	ins.mu.Lock()
	defer ins.mu.Unlock()
	stopForwarders(ins)
}

func stopForwarders(ins *Instance) {
	ins.fwdMu.Lock()
	fwds := make([]*forward.Forwarder, 0, len(ins.forwarders))
	for _, f := range ins.forwarders {
		fwds = append(fwds, f)
	}
	ins.forwarders = make(map[uint]*forward.Forwarder)
	ins.fwdMu.Unlock()
	for _, f := range fwds {
		f.Stop()
	}
}

// ForwardStatuses returns every rule of an instance with live state.
// Rules whose forwarder never started (tunnel stopped) report
// Listening=false with zero stats, which the UI renders as "stopped".
func (m *Manager) ForwardStatuses(id uint) []ForwardStatus {
	var inst db.TunnelInstance
	if err := m.db.First(&inst, id).Error; err != nil {
		return nil
	}
	var rules []db.ForwardRule
	if err := m.db.Where("instance_id = ?", id).Order("listen_port").Find(&rules).Error; err != nil {
		return nil
	}
	ins := m.getInstance(id)
	ins.fwdMu.Lock()
	defer ins.fwdMu.Unlock()
	out := make([]ForwardStatus, 0, len(rules))
	for _, rule := range rules {
		st := ForwardStatus{Rule: rule, Target: targetAddr(inst, rule)}
		if fwd, ok := ins.forwarders[rule.ID]; ok {
			st.Stats = fwd.Stats()
			st.Listening = st.Stats.Listening
		}
		out = append(out, st)
	}
	return out
}
