package manager

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ParsaKSH/spoof-tunnel/internal/l3"
	"github.com/ParsaKSH/spoof-tunnel/panel/internal/db"
	"github.com/ParsaKSH/spoof-tunnel/panel/internal/forward"
	"gorm.io/gorm"
)

// TunnelStatus represents the tunnel process status
type TunnelStatus string

const (
	StatusStopped  TunnelStatus = "stopped"
	StatusRunning  TunnelStatus = "running"
	StatusStarting TunnelStatus = "starting"
	StatusStopping TunnelStatus = "stopping"
	StatusError    TunnelStatus = "error"
)

// Instance represents a single running tunnel process
type Instance struct {
	processGeneration uint64
	wanted            bool
	epoch             uint64
	retryCancel       chan struct{}
	retryExhausted    bool
	retryNextAt       time.Time
	retryCount        int
	recovering        bool
	actionMu          sync.Mutex
	ID                uint
	cmd               *exec.Cmd
	done              chan struct{}
	telemetry         l3.Stats
	status            TunnelStatus
	error             string
	mu                sync.Mutex
	logLines          []string
	logMu             sync.RWMutex
	maxLogs           int
	startTime         time.Time

	// Log broadcasting to multiple WebSocket subscribers
	subMu sync.Mutex
	subID int
	subs  map[int]chan string

	// Reverse-tunnel forwarders for this instance, by rule ID.
	fwdMu      sync.Mutex
	forwarders map[uint]*forward.Forwarder
}

func newInstance(id uint) *Instance {
	return &Instance{
		ID:         id,
		status:     StatusStopped,
		logLines:   make([]string, 0, 1000),
		maxLogs:    1000,
		subs:       make(map[int]chan string),
		forwarders: make(map[uint]*forward.Forwarder),
	}
}

// Manager manages multiple tunnel instances
type Manager struct {
	recoveryDelay func(int) time.Duration // fixed at construction; tests may shorten backoff
	mutationGuard *sync.Mutex
	db            *gorm.DB
	binaryPath    string
	configDir     string
	instances     map[uint]*Instance
	mu            sync.RWMutex
}

// NewManager creates a new multi-instance tunnel manager
func NewManager(database *gorm.DB, binaryPath, configDir string) *Manager {
	return &Manager{
		recoveryDelay: recoveryBackoff,
		mutationGuard: new(sync.Mutex),
		db:            database,
		binaryPath:    binaryPath,
		configDir:     configDir,
		instances:     make(map[uint]*Instance),
	}
}

// getInstance returns or creates an Instance tracker for the given ID
func (m *Manager) getInstance(id uint) *Instance {
	m.mu.Lock()
	defer m.mu.Unlock()
	inst, ok := m.instances[id]
	if !ok {
		inst = newInstance(id)
		m.instances[id] = inst
	}
	return inst
}

// InstanceStatus returns current status for an instance
func (m *Manager) InstanceStatus(id uint) (TunnelStatus, string) {
	inst := m.getInstance(id)
	inst.mu.Lock()
	defer inst.mu.Unlock()
	return inst.status, inst.error
}

// StartInstance starts a tunnel instance
func (m *Manager) StartInstance(id uint) error {
	inst := m.getInstance(id)
	inst.actionMu.Lock()
	defer inst.actionMu.Unlock()
	inst.mu.Lock()
	if inst.cmd != nil {
		inst.mu.Unlock()
		return fmt.Errorf("instance %d already running", id)
	}
	inst.newIntent(true)
	inst.mu.Unlock()
	if err := m.startInstance(id); err != nil {
		inst.mu.Lock()
		inst.wanted = false
		inst.mu.Unlock()
		return err
	}
	m.StartForwarders(id)
	return nil
}

func (m *Manager) startInstance(id uint) (err error) {
	inst := m.getInstance(id)
	inst.mu.Lock()
	defer func() {
		if err != nil && inst.cmd == nil {
			inst.status = StatusError
			inst.error = err.Error()
		}
		inst.mu.Unlock()
	}()

	if inst.cmd != nil {
		return fmt.Errorf("instance %d already running", id)
	}

	// Load config from DB
	var cfg db.TunnelInstance
	if err := m.db.First(&cfg, id).Error; err != nil {
		return fmt.Errorf("instance %d not found: %w", id, err)
	}

	if err := cfg.Validate(); err != nil {
		return err
	}

	// Generate config file
	configPath, err := m.generateConfig(cfg)
	if err != nil {
		return fmt.Errorf("generate config: %w", err)
	}

	// Write spoof IP file if needed
	if cfg.SpoofIPList != "" {
		spoofPath := m.spoofIPFilePath(id)
		os.MkdirAll(filepath.Dir(spoofPath), 0755)
		if err := os.WriteFile(spoofPath, []byte(cfg.SpoofIPList+"\n"), 0644); err != nil {
			return fmt.Errorf("write spoof IPs: %w", err)
		}
	}

	inst.status = StatusStarting

	// Start the binary
	if cfg.Engine == "tun" {
		inst.cmd = exec.Command(m.resolveL3Binary(), "--config", configPath)
	} else {
		inst.cmd = exec.Command(m.binaryPath, "run", "--config", configPath)
	}
	inst.processGeneration++
	generation := inst.processGeneration
	inst.telemetry = l3.Stats{}
	inst.cmd.Env = append(os.Environ(), "GODEBUG=madvdontneed=1")

	stdout, err := inst.cmd.StdoutPipe()
	if err != nil {
		inst.status = StatusError
		inst.error = err.Error()
		inst.cmd = nil
		return err
	}
	stderr, err := inst.cmd.StderrPipe()
	if err != nil {
		inst.status = StatusError
		inst.error = err.Error()
		inst.cmd = nil
		return err
	}

	if err := inst.cmd.Start(); err != nil {
		inst.status = StatusError
		inst.error = err.Error()
		inst.cmd = nil
		return err
	}

	inst.status = StatusRunning
	inst.error = ""
	inst.startTime = time.Now()

	// Preserve the crash context across automatic recovery; manual starts reset it.
	if !inst.recovering {
		inst.logMu.Lock()
		inst.logLines = make([]string, 0, 1000)
		inst.logMu.Unlock()
	}

	go streamLogs(inst, stdout, generation)
	go streamLogs(inst, stderr, generation)

	cmd := inst.cmd
	epoch := inst.epoch
	inst.done = make(chan struct{})
	done := inst.done
	go func() {
		err := cmd.Wait()
		// If the tunnel process died on its own, its entry forwarders
		// must not keep accepting clients into a dead TUN.
		inst.mu.Lock()
		stopForwarders(inst)
		if inst.cmd == cmd {
			inst.cmd = nil
			if inst.status == StatusStopping {
				err = nil
			}
			inst.status = StatusStopped
			if err != nil {
				inst.status = StatusError
				inst.error = err.Error()
			}
		}
		restart := inst.wanted && inst.epoch == epoch
		if time.Since(inst.startTime) > 30*time.Second {
			inst.retryCount = 0
		}
		close(done)
		inst.mu.Unlock()
		if restart {
			go func() { inst.actionMu.Lock(); inst.actionMu.Unlock(); m.recoverInstance(inst, epoch) }()
		}
	}()

	log.Printf("[manager] started instance %d (%s)", id, cfg.Name)
	// Entry forwarders only make sense once the TUN is up; a failed
	// bind is logged by StartForwarders and shown in the UI, it does
	// not roll back the tunnel itself.
	return nil
}

// StopInstance stops a tunnel instance
func (m *Manager) StopInstance(id uint) error {
	inst := m.getInstance(id)
	inst.actionMu.Lock()
	defer inst.actionMu.Unlock()
	inst.mu.Lock()
	inst.newIntent(false)
	inst.mu.Unlock()
	return m.stopInstance(id)
}

func (m *Manager) stopInstance(id uint) error {
	inst := m.getInstance(id)
	inst.mu.Lock()
	// Serialize the state transition and listener shutdown with forward starts.
	stopForwarders(inst)
	if inst.cmd == nil || inst.cmd.Process == nil {
		inst.status = StatusStopped
		inst.mu.Unlock()
		return nil
	}
	inst.status = StatusStopping
	process, done := inst.cmd.Process, inst.done
	inst.mu.Unlock()
	// Interrupt lets the core release its TUN device and raw sockets.
	err := process.Signal(os.Interrupt)
	if err != nil && err != os.ErrProcessDone {
		_ = process.Kill()
	}
	select {
	case <-done:
		return nil
	case <-time.After(5 * time.Second):
		if err := process.Kill(); err != nil && err != os.ErrProcessDone {
			return err
		}
	}
	select {
	case <-done:
		return nil
	case <-time.After(5 * time.Second):
		return fmt.Errorf("process did not exit")
	}
}

func (m *Manager) RestartInstance(id uint) error {
	inst := m.getInstance(id)
	inst.actionMu.Lock()
	defer inst.actionMu.Unlock()
	inst.mu.Lock()
	inst.newIntent(true)
	inst.mu.Unlock()
	if err := m.stopInstance(id); err != nil {
		return err
	}
	if err := m.startInstance(id); err != nil {
		return err
	}
	m.StartForwarders(id)
	return nil
}

// InstanceUptime returns uptime for an instance
func (m *Manager) InstanceUptime(id uint) time.Duration {
	inst := m.getInstance(id)
	inst.mu.Lock()
	defer inst.mu.Unlock()
	if inst.status != StatusRunning {
		return 0
	}
	return time.Since(inst.startTime)
}

// InstanceLogs returns recent log lines for an instance
func (m *Manager) InstanceLogs(id uint, n int) []string {
	inst := m.getInstance(id)
	inst.logMu.RLock()
	defer inst.logMu.RUnlock()

	if n <= 0 || n > len(inst.logLines) {
		n = len(inst.logLines)
	}
	start := len(inst.logLines) - n
	result := make([]string, n)
	copy(result, inst.logLines[start:])
	return result
}

// SubscribeLogs creates a new log subscription channel for an instance
func (m *Manager) SubscribeLogs(id uint) (int, <-chan string) {
	inst := m.getInstance(id)
	inst.subMu.Lock()
	defer inst.subMu.Unlock()
	inst.subID++
	ch := make(chan string, 100)
	inst.subs[inst.subID] = ch
	return inst.subID, ch
}

// UnsubscribeLogs removes a log subscription
func (m *Manager) UnsubscribeLogs(id uint, subID int) {
	inst := m.getInstance(id)
	inst.subMu.Lock()
	defer inst.subMu.Unlock()
	if ch, ok := inst.subs[subID]; ok {
		close(ch)
		delete(inst.subs, subID)
	}
}

// RemoveInstance cleans up instance state after deletion
func (m *Manager) RemoveInstance(id uint) {
	m.StopInstance(id)
	m.mu.Lock()
	delete(m.instances, id)
	m.mu.Unlock()

	// Clean up files
	os.Remove(m.configFilePath(id))
	os.Remove(m.spoofIPFilePath(id))
	os.Remove(filepath.Join(m.configDir, fmt.Sprintf("pool-state-%d.json", id)))
}

// StopAll stops all running instances
func (m *Manager) StopAll() {
	m.mu.RLock()
	ids := make([]uint, 0, len(m.instances))
	for id := range m.instances {
		ids = append(ids, id)
	}
	m.mu.RUnlock()

	for _, id := range ids {
		m.StopInstance(id)
	}
}

// AllStatuses returns status for all known instances
func (m *Manager) AllStatuses() map[uint]TunnelStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make(map[uint]TunnelStatus, len(m.instances))
	for id, inst := range m.instances {
		inst.mu.Lock()
		result[id] = inst.status
		inst.mu.Unlock()
	}
	return result
}

// ── Legacy compatibility ──

// Status returns the status of the first instance (for backward compat)
func (m *Manager) Status() (TunnelStatus, string) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, inst := range m.instances {
		inst.mu.Lock()
		s, e := inst.status, inst.error
		inst.mu.Unlock()
		return s, e
	}
	return StatusStopped, ""
}

// Uptime returns the uptime of the first instance
func (m *Manager) Uptime() time.Duration {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, inst := range m.instances {
		inst.mu.Lock()
		if inst.status != StatusRunning {
			inst.mu.Unlock()
			return 0
		}
		d := time.Since(inst.startTime)
		inst.mu.Unlock()
		return d
	}
	return 0
}

// GetLogs returns logs from the first instance
func (m *Manager) GetLogs(n int) []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for id := range m.instances {
		return m.InstanceLogs(id, n)
	}
	return nil
}

// LogChannel is deprecated — use SubscribeLogs/UnsubscribeLogs instead

// BinaryPath returns the path to the spoof binary
func (m *Manager) BinaryPath() string {
	return m.binaryPath
}

// ── Internal ──

func (m *Manager) resolveL3Binary() string {
	candidates := []string{
		filepath.Join(m.configDir, "spoof-l3"),
		filepath.Join(filepath.Dir(m.binaryPath), "spoof-l3"),
		"/etc/spoof-panel/spoof-l3",
		"/usr/local/bin/spoof-l3",
		"spoof-l3",
	}
	for _, p := range candidates {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	if p, err := exec.LookPath("spoof-l3"); err == nil {
		return p
	}
	return filepath.Join(m.configDir, "spoof-l3")
}

func (m *Manager) configFilePath(id uint) string {
	return filepath.Join(m.configDir, fmt.Sprintf("tunnel-config-%d.json", id))
}

func (m *Manager) spoofIPFilePath(id uint) string {
	return filepath.Join(m.configDir, fmt.Sprintf("spoof-ips-%d.txt", id))
}

func (m *Manager) generateConfig(cfg db.TunnelInstance) (string, error) {
	tunnelCfg := map[string]interface{}{
		"mode":           cfg.Mode,
		"send_transport": cfg.SendTransport,
		"recv_transport": cfg.RecvTransport,
		"spoof_ip":       cfg.SpoofIP,
		"spoof_port":     cfg.SpoofPort,
		"peer_spoof_ip":  cfg.PeerSpoofIP,
	}

	// If inline spoof IPs exist, write them to a file and reference it
	if strings.TrimSpace(cfg.SpoofIPList) != "" {
		spoofPath := m.spoofIPFilePath(cfg.ID)
		tunnelCfg["spoof_ip_file"] = spoofPath
	}

	switch cfg.Mode {
	case "local":
		tunnelCfg["listen"] = cfg.ListenAddr
		tunnelCfg["remote"] = cfg.RemoteAddr
		tunnelCfg["remote_port"] = cfg.RemotePort
		tunnelCfg["recv_port"] = cfg.RecvPort
	case "remote":
		tunnelCfg["listen_port"] = cfg.ListenPort
		tunnelCfg["forward"] = cfg.ForwardAddr
		tunnelCfg["client_ip"] = cfg.ClientIP
		tunnelCfg["client_port"] = cfg.ClientPort
	}

	var value interface{} = tunnelCfg
	if cfg.Engine == "tun" {
		l3cfg := cfg.L3Config()
		l3cfg.ControlSocket = m.PoolSocket(cfg.ID)
		l3cfg.PoolStateFile = filepath.Join(m.configDir, fmt.Sprintf("pool-state-%d.json", cfg.ID))
		value = l3cfg
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return "", err
	}

	configPath := m.configFilePath(cfg.ID)
	if err := os.MkdirAll(filepath.Dir(configPath), 0700); err != nil {
		return "", err
	}

	log.Printf("[manager] writing config for instance %d to %s", cfg.ID, configPath)
	return configPath, os.WriteFile(configPath, data, 0600)
}

func streamLogs(inst *Instance, reader io.Reader, generation ...uint64) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if pos := strings.Index(line, "{"); pos >= 0 {
			var stats l3.Stats
			if json.Unmarshal([]byte(line[pos:]), &stats) == nil && (stats.Event == "spoof_stats" || stats.Event == "emad_stats") {
				inst.mu.Lock()
				if len(generation) == 0 || inst.processGeneration == generation[0] {
					inst.telemetry = stats
				}
				inst.mu.Unlock()
			}
		}

		inst.logMu.Lock()
		inst.logLines = append(inst.logLines, line)
		if len(inst.logLines) > inst.maxLogs {
			inst.logLines = inst.logLines[len(inst.logLines)-inst.maxLogs:]
		}
		inst.logMu.Unlock()

		// Broadcast to all subscribers
		inst.subMu.Lock()
		for _, ch := range inst.subs {
			select {
			case ch <- line:
			default:
			}
		}
		inst.subMu.Unlock()
	}
}

func (m *Manager) Telemetry(id uint) l3.Stats {
	inst := m.getInstance(id)
	inst.mu.Lock()
	defer inst.mu.Unlock()
	s := inst.telemetry
	s.Peer = inst.status == StatusRunning && s.LastPeer > time.Now().Add(-15*time.Second).Unix()
	if s.PoolMode != "" && s.PoolMode != "legacy" {
		s.Peer = s.Peer && s.PoolReady
	}
	return s
}

// Called with mu held. A new explicit action invalidates every queued recovery.
func (inst *Instance) newIntent(wanted bool) {
	if inst.retryCancel != nil {
		close(inst.retryCancel)
	}
	inst.retryCancel = make(chan struct{})
	inst.epoch++
	inst.wanted = wanted
	inst.retryCount = 0
	inst.retryExhausted = false
	inst.retryNextAt = time.Time{}
	inst.recovering = false
}
func (m *Manager) recoverInstance(inst *Instance, epoch uint64) {
	inst.mu.Lock()
	if !inst.wanted || inst.epoch != epoch || inst.recovering {
		inst.mu.Unlock()
		return
	}
	inst.recovering = true
	cancel := inst.retryCancel
	inst.mu.Unlock()

	for {
		inst.mu.Lock()
		if !inst.wanted || inst.epoch != epoch {
			inst.mu.Unlock()
			return
		}
		if inst.retryCount >= maxRecoveryAttempts {
			inst.wanted, inst.recovering, inst.retryExhausted = false, false, true
			inst.retryNextAt = time.Time{}
			inst.status = StatusError
			inst.error = fmt.Sprintf("automatic recovery stopped after %d attempts; inspect logs/configuration and start manually: %s", maxRecoveryAttempts, inst.error)
			message := inst.error
			inst.mu.Unlock()
			streamLogs(inst, strings.NewReader("[supervisor] "+message+"\n"))
			return
		}
		number := inst.retryCount + 1
		delay := m.recoveryDelay(inst.retryCount)
		inst.retryNextAt = time.Now().Add(delay)
		inst.mu.Unlock()
		log.Printf("[manager] instance %d recovery in %s", inst.ID, delay)
		streamLogs(inst, strings.NewReader(fmt.Sprintf("[supervisor] automatic recovery in %s (attempt %d)\n", delay, number)))
		timer := time.NewTimer(delay)
		select {
		case <-cancel:
			timer.Stop()
			return
		case <-timer.C:
		}
		m.mu.RLock()
		guard := m.mutationGuard
		m.mu.RUnlock()
		if guard != nil {
			guard.Lock()
		}
		inst.actionMu.Lock()
		inst.mu.Lock()
		valid := inst.wanted && inst.epoch == epoch && inst.cmd == nil
		inst.mu.Unlock()
		if !valid {
			inst.mu.Lock()
			if inst.epoch == epoch {
				inst.recovering = false
			}
			inst.mu.Unlock()
			inst.actionMu.Unlock()
			if guard != nil {
				guard.Unlock()
			}
			return
		}
		inst.mu.Lock()
		inst.retryCount++
		inst.retryNextAt = time.Time{}
		inst.mu.Unlock()
		err := m.startInstance(inst.ID)
		if err == nil {
			m.StartForwarders(inst.ID)
		}
		// A fast new exit must be allowed to schedule its own recovery.
		if err == nil {
			inst.mu.Lock()
			if inst.epoch == epoch {
				inst.recovering = false
			}
			inst.mu.Unlock()
		}
		inst.actionMu.Unlock()
		if guard != nil {
			guard.Unlock()
		}
		if err == nil {
			return
		}
		log.Printf("[manager] instance %d recovery failed: %v", inst.ID, err)
	}
}

// MutationGuard is the stable control-plane lock shared with API transactions.
func (m *Manager) MutationGuard() *sync.Mutex { return m.mutationGuard }

const maxRecoveryAttempts = 8

func recoveryBackoff(attempt int) time.Duration {
	if attempt > 5 {
		attempt = 5
	}
	delay := time.Second * time.Duration(1<<attempt)
	if delay > 30*time.Second {
		return 30 * time.Second
	}
	return delay
}

type RecoveryState struct {
	Active      bool  `json:"active"`
	Attempts    int   `json:"attempts"`
	MaxAttempts int   `json:"max_attempts"`
	NextRetryAt int64 `json:"next_retry_at"`
	Exhausted   bool  `json:"exhausted"`
}

func (m *Manager) RecoveryStatus(id uint) RecoveryState {
	inst := m.getInstance(id)
	inst.mu.Lock()
	defer inst.mu.Unlock()
	state := RecoveryState{Active: inst.recovering, Attempts: inst.retryCount, MaxAttempts: maxRecoveryAttempts, Exhausted: inst.retryExhausted}
	if !inst.retryNextAt.IsZero() {
		state.NextRetryAt = inst.retryNextAt.Unix()
	}
	return state
}

func (m *Manager) PoolSocket(id uint) string {
	return filepath.Join(m.configDir, fmt.Sprintf("pool-control-%d.sock", id))
}
