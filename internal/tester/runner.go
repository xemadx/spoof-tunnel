package tester

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net"
	"os"
	"sync"
)

// TesterConfig holds the configuration for a tester run.
type TesterConfig struct {
	Mode          string  `json:"mode"`            // "sender" or "receiver"
	Protocol      string  `json:"protocol"`        // "tcp" or "icmp"
	DstIP         string  `json:"dst_ip"`          // destination IP (sender mode)
	DstPort       int     `json:"dst_port"`        // destination port (TCP only)
	Timeout       int     `json:"timeout"`         // receiver timeout in seconds
	PacketCount   int     `json:"packet_count"`    // packets per source IP
	MaxPacketLoss float64 `json:"max_packet_loss"` // max allowed loss %
	// Concurrency is accepted for API compatibility but intentionally
	// unused: sending is sequential so per-IP counts stay exact. The
	// panel no longer offers a concurrency control (it used to promise
	// parallelism that never happened).
	Concurrency int `json:"concurrency"`
}

// TesterResult holds the result for a single IP.
type TesterResult struct {
	IP       string  `json:"ip"`
	Received int     `json:"received"`
	Sent     int     `json:"sent"`
	LossPct  float64 `json:"loss_pct"`
	Passed   bool    `json:"passed"`
}

// TesterState represents the overall state.
type TesterState struct {
	Status        string         `json:"status"` // "idle", "running", "done", "error"
	Mode          string         `json:"mode"`
	Error         string         `json:"error,omitempty"`
	Progress      int            `json:"progress"` // 0-100
	ProcessedIPs  int64          `json:"processed_ips"`
	SentPackets   int64          `json:"sent_packets"`
	FailedPackets int64          `json:"failed_packets"`
	TotalIPs      int64          `json:"total_ips"`
	PassedCount   int            `json:"passed_count"`
	RecvCount     int            `json:"recv_count"`
	Results       []TesterResult `json:"results,omitempty"`
}

// Runner manages a tester execution.
type Runner struct {
	lifecycle   sync.Mutex
	doneCh      chan struct{}
	mu          sync.Mutex
	state       TesterState
	cancelCh    chan struct{}
	resultsFile string // temp file holding JSON-lines results
	tmpDir      string

	// Live receiver state — accessible during test for real-time monitoring
	recvMu        sync.RWMutex
	received      map[uint32]int
	receivedOrder []uint32
	recvPassed    int
	recvCfgPkt    int     // packetCount for current run
	recvCfgLoss   float64 // maxPacketLoss for current run
}

// NewRunner creates a new tester runner.
func NewRunner() *Runner {
	return &Runner{
		state: TesterState{Status: "idle"},
	}
}

// SetTmpDir sets the directory for temporary result files.
func (r *Runner) SetTmpDir(dir string) {
	r.lifecycle.Lock()
	defer r.lifecycle.Unlock()
	r.mu.Lock()
	r.tmpDir = dir
	r.mu.Unlock()
}

func (r *Runner) getTmpDir() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.tmpDir != "" {
		return r.tmpDir
	}
	return os.TempDir()
}

// State returns current state (without full results — use Results() for that).
func (r *Runner) State() TesterState {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.state

	// If receiver is running, update live recv count
	if s.Status == "running" && s.Mode == "receiver" {
		r.recvMu.RLock()
		s.RecvCount = len(r.received)
		s.PassedCount = r.recvPassed
		r.recvMu.RUnlock()
	}

	if s.Results == nil {
		s.Results = []TesterResult{}
	}
	return s
}

// LiveResults returns the current received IPs during a running receiver test.
// Returns results for all IPs that have received at least one packet so far.
func (r *Runner) LiveResults() []TesterResult { return r.liveResults(0) }

func (r *Runner) liveResults(limit int) []TesterResult {
	r.recvMu.RLock()
	defer r.recvMu.RUnlock()

	if r.received == nil || len(r.received) == 0 {
		return []TesterResult{}
	}

	pktCount := r.recvCfgPkt
	maxLoss := r.recvCfgLoss
	ipBuf := make(net.IP, 4)

	size := len(r.receivedOrder)
	if limit > 0 && size > limit {
		size = limit
	}
	results := make([]TesterResult, 0, size)
	for _, ipU32 := range r.receivedOrder[:size] {
		count := r.received[ipU32]
		binary.BigEndian.PutUint32(ipBuf, ipU32)
		lossPct := float64(pktCount-count) / float64(pktCount) * 100.0
		if lossPct < 0 {
			lossPct = 0
		}
		results = append(results, TesterResult{
			IP:       ipBuf.String(),
			Received: count,
			Sent:     pktCount,
			LossPct:  lossPct,
			Passed:   lossPct <= maxLoss,
		})
	}
	return results
}

// Results reads results from the temp file (post-completion) or returns
// live results if the receiver is still running.
func (r *Runner) Results() ([]TesterResult, error) { return r.ResultsLimit(0) }

// ResultsLimit bounds polling payloads; explicit exports can request all results.
func (r *Runner) ResultsLimit(limit int) ([]TesterResult, error) {
	r.mu.Lock()
	status := r.state.Status
	mode := r.state.Mode
	path := r.resultsFile
	r.mu.Unlock()

	// If receiver is still running, return live results
	if status == "running" && mode == "receiver" {
		return r.liveResults(limit), nil
	}

	if path == "" {
		return []TesterResult{}, nil
	}

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []TesterResult{}, nil
		}
		return nil, err
	}
	defer f.Close()

	var results []TesterResult
	dec := json.NewDecoder(f)
	for dec.More() {
		var res TesterResult
		if err := dec.Decode(&res); err != nil {
			break
		}
		results = append(results, res)
		if limit > 0 && len(results) >= limit {
			break
		}
	}
	return results, nil
}

// PassedIPs returns only the IPs that passed the test.
func (r *Runner) PassedIPs() ([]string, error) {
	results, err := r.Results()
	if err != nil {
		return nil, err
	}
	var ips []string
	for _, res := range results {
		if res.Passed {
			ips = append(ips, res.IP)
		}
	}
	return ips, nil
}

// Stop cancels a running test.
func (r *Runner) Stop() {
	r.lifecycle.Lock()
	defer r.lifecycle.Unlock()
	r.mu.Lock()
	done := r.doneCh
	if r.cancelCh != nil {
		close(r.cancelCh)
		r.cancelCh = nil
	}
	r.mu.Unlock()
	if done != nil {
		<-done
	}
	r.mu.Lock()
	if r.state.Status == "done" || r.state.Status == "running" {
		r.state.Status = "idle"
	}
	r.mu.Unlock()
}

func validateConfig(cfg TesterConfig, ranges *IPRangeSet, sender bool) error {
	if ranges == nil || ranges.Total() == 0 {
		return fmt.Errorf("IP list is empty")
	}
	if cfg.Protocol != "tcp" && cfg.Protocol != "icmp" {
		return fmt.Errorf("protocol must be tcp or icmp")
	}
	if cfg.PacketCount < 1 || cfg.PacketCount > 1000 || cfg.Timeout < 1 || cfg.Timeout > 3600 {
		return fmt.Errorf("packet_count must be 1–1000 and timeout 1–3600 seconds")
	}
	if math.IsNaN(cfg.MaxPacketLoss) || cfg.MaxPacketLoss < 0 || cfg.MaxPacketLoss > 100 {
		return fmt.Errorf("max_packet_loss must be 0–100")
	}
	if cfg.DstPort < 0 || cfg.DstPort > 65535 || (sender && cfg.Protocol == "tcp" && cfg.DstPort == 0) {
		return fmt.Errorf("invalid destination port")
	}
	if sender {
		ip := net.ParseIP(cfg.DstIP)
		if ip == nil || ip.To4() == nil || ip.IsUnspecified() || ip.IsMulticast() || ip.Equal(net.IPv4bcast) {
			return fmt.Errorf("destination must be unicast IPv4")
		}
	}
	return nil
}

// RunSender starts the sender in background using compact IPRangeSet.
func (r *Runner) RunSender(cfg TesterConfig, ranges *IPRangeSet) error {
	r.lifecycle.Lock()
	defer r.lifecycle.Unlock()
	if err := validateConfig(cfg, ranges, true); err != nil {
		return err
	}
	r.mu.Lock()
	if r.state.Status == "running" {
		r.mu.Unlock()
		return fmt.Errorf("tester already running")
	}
	r.state = TesterState{Status: "running", Mode: "sender", TotalIPs: ranges.Total()}
	r.cancelCh = make(chan struct{})
	cancelCh := r.cancelCh
	done := make(chan struct{})
	r.doneCh = done
	// Clean old results
	if r.resultsFile != "" {
		os.Remove(r.resultsFile)
		r.resultsFile = ""
	}
	r.mu.Unlock()

	go func() {
		defer close(done)
		err := r.doSend(cfg, ranges, cancelCh)
		r.mu.Lock()
		select {
		case <-cancelCh:
			r.state.Status = "idle"
			r.mu.Unlock()
			return
		default:
		}
		if err != nil {
			r.state.Status = "error"
			r.state.Error = err.Error()
		} else {
			r.state.Status = "done"
			r.state.Progress = 100
		}
		r.mu.Unlock()
	}()

	return nil
}

// RunReceiver starts the receiver in background using compact IPRangeSet.
func (r *Runner) RunReceiver(cfg TesterConfig, ranges *IPRangeSet) error {
	r.lifecycle.Lock()
	defer r.lifecycle.Unlock()
	if err := validateConfig(cfg, ranges, false); err != nil {
		return err
	}
	r.mu.Lock()
	if r.state.Status == "running" {
		r.mu.Unlock()
		return fmt.Errorf("tester already running")
	}
	r.state = TesterState{Status: "running", Mode: "receiver", TotalIPs: ranges.Total()}
	r.cancelCh = make(chan struct{})
	cancelCh := r.cancelCh
	done := make(chan struct{})
	r.doneCh = done
	// Clean old results
	if r.resultsFile != "" {
		os.Remove(r.resultsFile)
		r.resultsFile = ""
	}
	// Initialize live receiver state
	r.recvMu.Lock()
	r.received = make(map[uint32]int)
	r.receivedOrder = nil
	r.recvPassed = 0
	pktCount := cfg.PacketCount
	if pktCount < 1 {
		pktCount = 10
	}
	r.recvCfgPkt = pktCount
	r.recvCfgLoss = cfg.MaxPacketLoss
	r.recvMu.Unlock()

	r.mu.Unlock()

	go func() {
		defer close(done)
		err := r.doReceive(cfg, ranges, cancelCh)
		r.mu.Lock()
		if err != nil {
			r.state.Status = "error"
			r.state.Error = err.Error()
		} else if r.state.Status == "running" {
			r.state.Status = "done"
			r.state.Progress = 100
		}
		r.mu.Unlock()
	}()

	return nil
}

// flushInterimResults writes current received IPs to the results file.
// Called periodically (every 5 min) and at the end of the test.
func (r *Runner) flushInterimResults(packetCount int, maxLoss float64) {
	r.recvMu.RLock()
	if len(r.received) == 0 {
		r.recvMu.RUnlock()
		return
	}

	// Copy received map under read lock
	snapshot := make(map[uint32]int, len(r.received))
	for k, v := range r.received {
		snapshot[k] = v
	}
	r.recvMu.RUnlock()

	tmpDir := r.getTmpDir()
	if err := os.MkdirAll(tmpDir, 0700); err != nil {
		log.Printf("[tester] result directory: %v", err)
		return
	}
	f, err := os.CreateTemp(tmpDir, "spoof-tester-*.jsonl")
	if err != nil {
		log.Printf("[tester-receiver] flush error: %v", err)
		return
	}

	enc := json.NewEncoder(f)
	passedCount := 0
	ipBuf := make(net.IP, 4)
	batch := 0

	for ipU32, count := range snapshot {
		binary.BigEndian.PutUint32(ipBuf, ipU32)
		lossPct := float64(packetCount-count) / float64(packetCount) * 100.0
		if lossPct < 0 {
			lossPct = 0
		}
		passed := lossPct <= maxLoss
		if passed {
			passedCount++
		}

		enc.Encode(TesterResult{
			IP:       ipBuf.String(),
			Received: count,
			Sent:     packetCount,
			LossPct:  lossPct,
			Passed:   passed,
		})
		batch++
		if batch%1000 == 0 {
			f.Sync()
		}
	}
	f.Close()

	r.mu.Lock()
	oldPath := r.resultsFile
	r.resultsFile = f.Name()
	r.state.PassedCount = passedCount
	r.state.RecvCount = len(snapshot)
	r.mu.Unlock()
	if oldPath != "" {
		_ = os.Remove(oldPath)
	}

	log.Printf("[tester-receiver] interim flush -- %d IPs received, %d passed", len(snapshot), passedCount)
}
