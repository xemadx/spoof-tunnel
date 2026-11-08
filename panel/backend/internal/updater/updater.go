package updater

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const Unit = "spoof-panel-update.service"
const repository = "https://api.github.com/repos/xemadx/spoof-tunnel"

var tagPattern = regexp.MustCompile(`^v(0|[1-9][0-9]{0,5})\.(0|[1-9][0-9]{0,5})\.(0|[1-9][0-9]{0,5})$`)
var idPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)

type Asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
	Size int64  `json:"size"`
}
type Release struct {
	Tag        string  `json:"tag_name"`
	URL        string  `json:"html_url"`
	Draft      bool    `json:"draft"`
	Prerelease bool    `json:"prerelease"`
	Assets     []Asset `json:"assets"`
}
type Job struct {
	BytesDone  int64     `json:"bytes_done,omitempty"`
	BytesTotal int64     `json:"bytes_total,omitempty"`
	ID         string    `json:"id"`
	From       string    `json:"from"`
	Target     string    `json:"target"`
	Phase      string    `json:"phase"`
	Progress   int       `json:"progress"`
	Error      string    `json:"error,omitempty"`
	StartedAt  time.Time `json:"started_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}
type Status struct {
	Current    string    `json:"current"`
	Latest     string    `json:"latest,omitempty"`
	ReleaseURL string    `json:"release_url,omitempty"`
	Available  bool      `json:"available"`
	Signed     bool      `json:"signed"`
	Supported  bool      `json:"supported"`
	Reason     string    `json:"reason,omitempty"`
	CheckedAt  time.Time `json:"checked_at"`
	Job        *Job      `json:"job,omitempty"`
}
type request struct {
	ID     string `json:"id"`
	Target string `json:"target"`
}
type Updater struct {
	allowed      func() (bool, string)
	workerActive func(context.Context) bool
	mu           sync.Mutex
	current      string
	dataDir      string
	panelPath    string
	corePath     string
	unitPath     string
	apiBase      string
	client       *http.Client
	command      func(context.Context, ...string) error
	health       func(context.Context, string, string) error
	latest       *Release
	checked      time.Time
}

func New(current, dataDir string) *Updater {
	u := &Updater{current: current, dataDir: dataDir, panelPath: "/opt/spoof-panel/spoof-panel", corePath: filepath.Join(dataDir, "spoof-l3"), unitPath: "/etc/systemd/system/" + Unit, apiBase: repository}
	u.client = &http.Client{Timeout: 0, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 8 || !downloadHost(req.URL) {
			return fmt.Errorf("unexpected download redirect")
		}
		return nil
	}}
	u.command = func(ctx context.Context, args ...string) error {
		out, err := exec.CommandContext(ctx, "/usr/bin/systemctl", args...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("systemctl failed: %s: %w", strings.TrimSpace(string(out)), err)
		}
		return nil
	}
	u.workerActive = func(ctx context.Context) bool {
		out, err := exec.CommandContext(ctx, "/usr/bin/systemctl", "show", "--property=ActiveState", "--value", Unit).Output()
		if err != nil {
			return false
		}
		state := strings.TrimSpace(string(out))
		return state == "active" || state == "activating" || state == "reloading"
	}
	u.health = u.waitHealth
	return u
}
func (u *Updater) dir() string { return filepath.Join(u.dataDir, "updates") }
func Active(phase string) bool {
	switch phase {
	case "queued", "checking", "downloading", "verifying", "backing_up", "installing", "restarting", "rolling_back":
		return true
	}
	return false
}
func newer(a, b string) bool {
	aa, bb := tagPattern.FindStringSubmatch(a), tagPattern.FindStringSubmatch(b)
	if aa == nil || bb == nil {
		return false
	}
	for i := 1; i <= 3; i++ {
		x, _ := strconv.Atoi(aa[i])
		y, _ := strconv.Atoi(bb[i])
		if x != y {
			return x > y
		}
	}
	return false
}
func downloadHost(v *url.URL) bool {
	return v.Scheme == "https" && v.User == nil && (v.Host == "github.com" || v.Host == "release-assets.githubusercontent.com" || v.Host == "objects.githubusercontent.com")
}
func (u *Updater) support() (bool, string) {
	if u.allowed != nil {
		return u.allowed()
	}
	if runtime.GOOS != "linux" || os.Geteuid() != 0 || (runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") {
		return false, "requires_linux_root"
	}
	if u.dataDir != "/etc/spoof-panel" || u.panelPath != "/opt/spoof-panel/spoof-panel" {
		return false, "requires_standard_install"
	}
	if _, err := os.Stat(u.unitPath); err != nil {
		return false, "missing_update_service"
	}
	group, err := os.ReadFile("/proc/self/cgroup")
	if err != nil || (!strings.Contains(string(group), "/spoof-panel.service") && !strings.Contains(string(group), "/spoof-panel-update.service")) {
		return false, "requires_standard_service"
	}
	if !tagPattern.MatchString(u.current) {
		return false, "unversioned_build"
	}
	return true, ""
}
func (u *Updater) Status() Status {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.status()
}
func (u *Updater) status() Status {
	supported, reason := u.support()
	s := Status{Current: u.current, Supported: supported, Reason: reason, CheckedAt: u.checked}
	if u.latest != nil {
		s.Latest = u.latest.Tag
		s.ReleaseURL = u.latest.URL
		s.Available = newer(s.Latest, s.Current)
		s.Signed = hasSignatureAsset(u.latest)
	}
	var job Job
	if readJSON(filepath.Join(u.dir(), "job.json"), &job) == nil {
		if Active(job.Phase) && time.Since(job.UpdatedAt) > 10*time.Second {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			active := u.workerActive(ctx)
			cancel()
			if !active {
				job.Phase = "failed"
				job.Error = "update_worker_stopped"
				job.UpdatedAt = time.Now()
				_ = atomicJSON(filepath.Join(u.dir(), "job.json"), job)
			}
		}
		s.Job = &job
	}
	return s
}
func (u *Updater) fetch(ctx context.Context, endpoint string) (*Release, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", u.apiBase+endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "Spoof-Panel-Updater")
	res, err := u.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github_unreachable")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("github_http_%d", res.StatusCode)
	}
	var release Release
	if err = json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&release); err != nil {
		return nil, fmt.Errorf("invalid_release")
	}
	if release.Draft || release.Prerelease || !tagPattern.MatchString(release.Tag) || len(release.Assets) > 32 {
		return nil, fmt.Errorf("invalid_release")
	}
	expected := "https://github.com/xemadx/spoof-tunnel/releases/tag/" + release.Tag
	if release.URL != expected {
		return nil, fmt.Errorf("invalid_release_url")
	}
	return &release, nil
}
func (u *Updater) Check(ctx context.Context) (Status, error) {
	release, err := u.fetch(ctx, "/releases/latest")
	if err != nil {
		return u.Status(), err
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.latest = release
	u.checked = time.Now()
	return u.status(), nil
}
func (u *Updater) Start(ctx context.Context, version string) (Status, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if ok, reason := u.support(); !ok {
		return u.status(), fmt.Errorf("%s", reason)
	}
	if s := u.status(); s.Job != nil && Active(s.Job.Phase) {
		return s, fmt.Errorf("update_already_running")
	}
	if u.workerActive(ctx) {
		return u.status(), fmt.Errorf("update_already_running")
	}
	release, err := u.fetch(ctx, "/releases/latest")
	if err != nil {
		return u.status(), err
	}
	if release.Tag != version || !newer(version, u.current) {
		return u.status(), fmt.Errorf("release_changed_or_not_newer")
	}
	if _, _, sig, err := u.assets(release); err != nil {
		return u.status(), err
	} else if requiresSignature(version) && sig.URL == "" {
		return u.status(), fmt.Errorf("missing_release_signature")
	}
	u.latest = release
	u.checked = time.Now()
	var nonce [16]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return u.status(), err
	}
	id := hex.EncodeToString(nonce[:])
	now := time.Now()
	job := Job{ID: id, From: u.current, Target: version, Phase: "queued", StartedAt: now, UpdatedAt: now}
	if err = os.MkdirAll(u.dir(), 0700); err != nil {
		return u.status(), err
	}
	if err = atomicJSON(filepath.Join(u.dir(), "request.json"), request{id, version}); err != nil {
		return u.status(), err
	}
	if err = atomicJSON(filepath.Join(u.dir(), "job.json"), job); err != nil {
		return u.status(), err
	}
	// This fixed unit runs outside the panel cgroup and survives its restart.
	if err = u.command(ctx, "start", "--no-block", Unit); err != nil {
		job.Phase = "failed"
		job.Error = "update_service_start_failed"
		job.UpdatedAt = time.Now()
		_ = atomicJSON(filepath.Join(u.dir(), "job.json"), job)
		return u.status(), err
	}
	return u.status(), nil
}
func hasSignatureAsset(r *Release) bool {
	for _, a := range r.Assets {
		if a.Name == "SHA256SUMS.sig" {
			return true
		}
	}
	return false
}

func (u *Updater) assets(r *Release) (Asset, Asset, Asset, error) {
	name := "spoof-tunnel-linux-" + runtime.GOARCH + ".tar.gz"
	var archive, sums, sig Asset
	for _, a := range r.Assets {
		if a.Name != name && a.Name != "SHA256SUMS" && a.Name != "SHA256SUMS.sig" {
			continue
		}
		expected := "https://github.com/xemadx/spoof-tunnel/releases/download/" + r.Tag + "/" + a.Name
		limit := int64(128 << 20)
		if a.Name == "SHA256SUMS.sig" {
			limit = 512
		} else if a.Name == "SHA256SUMS" {
			limit = 16384
		}
		if a.URL != expected || a.Size <= 0 || a.Size > limit {
			return archive, sums, sig, fmt.Errorf("invalid_release_asset")
		}
		switch a.Name {
		case name:
			if archive.URL != "" {
				return archive, sums, sig, fmt.Errorf("duplicate_release_asset")
			}
			archive = a
		case "SHA256SUMS":
			if sums.URL != "" {
				return archive, sums, sig, fmt.Errorf("duplicate_release_asset")
			}
			sums = a
		case "SHA256SUMS.sig":
			if sig.URL != "" {
				return archive, sums, sig, fmt.Errorf("duplicate_release_asset")
			}
			sig = a
		}
	}
	if archive.URL == "" || sums.URL == "" {
		return archive, sums, sig, fmt.Errorf("missing_release_assets")
	}
	return archive, sums, sig, nil
}
func atomicJSON(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return atomicFile(path, b, 0600)
}
func atomicFile(path string, data []byte, mode os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".update-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(name, path); err != nil {
		return err
	}
	if dir, err := os.Open(filepath.Dir(path)); err == nil {
		err = dir.Sync()
		dir.Close()
		return err
	}
	return nil
}
func readJSON(path string, v any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return json.NewDecoder(io.LimitReader(f, 16384)).Decode(v)
}
