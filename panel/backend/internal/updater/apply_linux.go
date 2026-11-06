//go:build linux

package updater

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"debug/elf"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	_ "github.com/glebarez/go-sqlite"
)

// Apply runs only in the independent, fixed systemd updater service.
func (u *Updater) Apply(ctx context.Context) (result error) {
	if ok, reason := u.support(); !ok {
		return fmt.Errorf("%s", reason)
	}
	lock, err := os.OpenFile(filepath.Join(u.dir(), "worker.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fmt.Errorf("update_already_running")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	var req request
	var job Job
	if readJSON(filepath.Join(u.dir(), "request.json"), &req) != nil || !idPattern.MatchString(req.ID) || !tagPattern.MatchString(req.Target) {
		return fmt.Errorf("invalid_update_request")
	}
	if readJSON(filepath.Join(u.dir(), "job.json"), &job) != nil || job.ID != req.ID || job.Target != req.Target || job.Phase != "queued" {
		return fmt.Errorf("invalid_update_job")
	}
	// A leftover intent means a previous worker died mid-update. The
	// panel reconciles it on startup; if this worker still sees one, the
	// panel has not restarted since, so refuse to stack another update
	// on top of the interrupted one.
	if u.hasPendingIntent() {
		return fmt.Errorf("previous_update_incomplete")
	}
	phase := func(name string, progress int) error {
		job.Phase = name
		job.Progress = progress
		job.UpdatedAt = time.Now()
		return atomicJSON(filepath.Join(u.dir(), "job.json"), job)
	}
	stage := filepath.Join(u.dir(), req.ID)
	stopped, installing, backedUp := false, false, false
	healthURL := ""
	defer func() {
		for _, name := range []string{"package.tar.gz", "spoof-panel", "spoof-l3", "package-checksums", "SHA256SUMS", "SHA256SUMS.sig"} {
			_ = os.Remove(filepath.Join(stage, name))
		}
		if result == nil {
			return
		}
		job.Error = result.Error()
		if stopped {
			_ = phase("rolling_back", 90)
			rollbackCtx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
			defer cancel()
			rollbackErr := error(nil)
			if installing {
				// Never restore a database while the new process could still write it.
				// The service is already stopped here. Restore the database
				// FIRST: a kill during rollback then leaves a consistent
				// database with at most mixed binaries, which the boot-time
				// reconcile can complete or roll back.
				rollbackErr = u.command(rollbackCtx, "stop", "spoof-panel.service")
				if rollbackErr == nil && backedUp {
					if e := replaceFile(filepath.Join(stage, "panel-backup.db"), filepath.Join(u.dataDir, "panel.db"), 0600); e != nil {
						rollbackErr = e
					} else {
						for _, suffix := range []string{"-wal", "-shm"} {
							if e := os.Remove(filepath.Join(u.dataDir, "panel.db") + suffix); e != nil && !os.IsNotExist(e) {
								rollbackErr = e
								break
							}
						}
					}
				}
				if rollbackErr == nil {
					for _, pair := range [][2]string{{"previous-panel", u.panelPath}, {"previous-core", u.corePath}} {
						if e := replaceFile(filepath.Join(stage, pair[0]), pair[1], 0755); e != nil {
							rollbackErr = e
							break
						}
					}
				}
			}
			if rollbackErr == nil {
				rollbackErr = u.command(rollbackCtx, "start", "spoof-panel.service")
			}
			// Verify the restored panel answers, also when the failure
			// happened after stop but before installing (for example the
			// database backup failed): a running service is not proof
			// that it serves the previous version.
			if rollbackErr == nil && healthURL != "" {
				rollbackErr = u.health(rollbackCtx, healthURL, job.From)
			}
			if rollbackErr == nil {
				job.Phase = "rolled_back"
				// The update is over; a leftover intent would only make
				// the next boot re-check an already restored pair.
				_ = os.Remove(filepath.Join(stage, "intent.json"))
			} else {
				job.Phase = "failed"
				job.Error += "; rollback_failed: " + rollbackErr.Error()
			}
		} else {
			job.Phase = "failed"
		}
		job.UpdatedAt = time.Now()
		_ = atomicJSON(filepath.Join(u.dir(), "job.json"), job)
	}()
	if err = os.Mkdir(stage, 0700); err != nil {
		return err
	}
	if err = phase("checking", 2); err != nil {
		return err
	}
	release, err := u.fetch(ctx, "/releases/tags/"+req.Target)
	if err != nil {
		return err
	}
	if release.Tag != req.Target || !newer(req.Target, u.current) {
		return fmt.Errorf("release_changed_or_not_newer")
	}
	archive, sums, sig, err := u.assets(release)
	if err != nil {
		return err
	}
	if err = phase("downloading", 10); err != nil {
		return err
	}
	sumsPath := filepath.Join(stage, "SHA256SUMS")
	if err = u.download(ctx, sums.URL, sumsPath, 16384); err != nil {
		return err
	}
	// The signature is checked before the archive checksum: a bad signature
	// means the checksums themselves cannot be trusted.
	if sig.URL != "" {
		sigPath := filepath.Join(stage, "SHA256SUMS.sig")
		if err = u.download(ctx, sig.URL, sigPath, 512); err != nil {
			return err
		}
		sumsBytes, err := os.ReadFile(sumsPath)
		if err != nil {
			return err
		}
		sigBytes, err := os.ReadFile(sigPath)
		if err != nil {
			return err
		}
		if err = verifyReleaseSignature(sumsBytes, sigBytes); err != nil {
			return err
		}
	} else if requiresSignature(req.Target) {
		return fmt.Errorf("missing_release_signature")
	}
	archivePath := filepath.Join(stage, "package.tar.gz")
	lastProgress := time.Time{}
	if err = u.download(ctx, archive.URL, archivePath, 128<<20, func(n int64) error {
		progress := 10 + int(30*n/archive.Size)
		if progress > 40 {
			progress = 40
		}
		job.BytesDone = n
		job.BytesTotal = archive.Size
		if n >= archive.Size || time.Since(lastProgress) > 500*time.Millisecond && progress != job.Progress {
			lastProgress = time.Now()
			return phase("downloading", progress)
		}
		return nil
	}); err != nil {
		return err
	}
	if err = phase("verifying", 45); err != nil {
		return err
	}
	hashes, err := checksumFile(sumsPath)
	if err != nil {
		return err
	}
	if err = verifyFile(archivePath, hashes[archive.Name]); err != nil {
		return fmt.Errorf("archive_checksum_failed: %w", err)
	}
	if err = extractBinaries(archivePath, stage, runtime.GOARCH); err != nil {
		return err
	}
	inner, err := checksumFile(filepath.Join(stage, "package-checksums"))
	if err != nil {
		return err
	}
	for _, name := range []string{"spoof-panel", "spoof-l3"} {
		candidate := filepath.Join(stage, name)
		if err = verifyFile(candidate, inner[name]); err != nil {
			return fmt.Errorf("binary_checksum_failed: %w", err)
		}
		file, e := elf.Open(candidate)
		if e != nil {
			return fmt.Errorf("invalid_executable")
		}
		machine := elf.EM_X86_64
		if runtime.GOARCH == "arm64" {
			machine = elf.EM_AARCH64
		}
		valid := file.Machine == machine && file.Class == elf.ELFCLASS64
		file.Close()
		if !valid {
			return fmt.Errorf("wrong_binary_architecture")
		}
		if err = os.Chmod(candidate, 0755); err != nil {
			return err
		}
		versionCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		versionCommand := exec.CommandContext(versionCtx, candidate, "--version")
		versionCommand.Env = append(os.Environ(), "SPOOF_DATA_DIR="+filepath.Join(stage, "version-check-data"))
		versionCommand.Dir = stage
		out, e := versionCommand.Output()
		cancel()
		want := req.Target
		if name == "spoof-l3" {
			want = "spoof-l3 " + want
		}
		if e != nil || strings.TrimSpace(string(out)) != want {
			return fmt.Errorf("binary_version_mismatch")
		}
	}
	// Downloads, archive checks and executable checks finish before stopping anything.
	// Resolve the health endpoint from the live database now: the rollback
	// path needs it even when the database backup below fails.
	if url, err := u.panelHealthURL(); err == nil {
		healthURL = url
	}
	if err = phase("backing_up", 60); err != nil {
		return err
	}
	if err = replaceFile(u.panelPath, filepath.Join(stage, "previous-panel"), 0755); err != nil {
		return err
	}
	if err = replaceFile(u.corePath, filepath.Join(stage, "previous-core"), 0755); err != nil {
		return err
	}
	stopped = true
	if err = u.command(ctx, "stop", "spoof-panel.service"); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if backupURL, err := backupDatabase(filepath.Join(u.dataDir, "panel.db"), filepath.Join(stage, "panel-backup.db")); err != nil {
		return err
	} else if healthURL == "" {
		healthURL = backupURL
	}
	backedUp = true
	if err = phase("installing", 75); err != nil {
		return err
	}
	installing = true
	// Marker for the boot-time reconcile: written before any live binary
	// is replaced and removed once the new pair answers its health check.
	if err = atomicJSON(filepath.Join(stage, "intent.json"), updateIntent{ID: req.ID, From: job.From, Target: req.Target}); err != nil {
		return err
	}
	if err = replaceFile(filepath.Join(stage, "spoof-l3"), u.corePath, 0755); err != nil {
		return err
	}
	if err = replaceFile(filepath.Join(stage, "spoof-panel"), u.panelPath, 0755); err != nil {
		return err
	}
	if err = phase("restarting", 90); err != nil {
		return err
	}
	if err = u.command(ctx, "start", "spoof-panel.service"); err != nil {
		return err
	}
	if err = u.health(ctx, healthURL, req.Target); err != nil {
		return err
	}
	if err = phase("succeeded", 100); err != nil {
		return err
	}
	// Keep the previous binaries and consistent DB snapshot for manual recovery.
	_ = os.Remove(filepath.Join(stage, "intent.json"))
	_ = os.Remove(filepath.Join(stage, "package.tar.gz"))
	_ = os.Remove(filepath.Join(u.dir(), "request.json"))
	return nil
}

// panelHealthURL reads the panel endpoint from the live database without
// modifying it, so the rollback path can verify the restored panel even
// when the database backup itself failed.
func (u *Updater) panelHealthURL() (string, error) {
	for _, dsn := range []string{
		"file:" + filepath.Join(u.dataDir, "panel.db") + "?mode=ro",
		filepath.Join(u.dataDir, "panel.db"),
	} {
		database, err := sql.Open("sqlite", dsn)
		if err != nil {
			continue
		}
		database.SetMaxOpenConns(1)
		var port, prefix string
		if err = database.QueryRow("SELECT value FROM settings WHERE key = 'panel_port'").Scan(&port); err == nil {
			err = database.QueryRow("SELECT value FROM settings WHERE key = 'web_path'").Scan(&prefix)
		}
		database.Close()
		if err != nil {
			continue
		}
		n, e := strconv.Atoi(port)
		if e != nil || n < 1 || n > 65535 || prefix == "" || strings.ContainsAny(prefix, "/?#\\\r\n") {
			return "", fmt.Errorf("invalid_panel_endpoint")
		}
		return "http://127.0.0.1:" + strconv.Itoa(n) + "/" + prefix + "/api/health", nil
	}
	return "", fmt.Errorf("invalid_panel_endpoint")
}

func (u *Updater) download(ctx context.Context, url, dest string, limit int64, progress ...func(int64) error) error {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "Spoof-Panel-Updater")
	res, err := u.client.Do(req)
	if err != nil {
		return fmt.Errorf("download_unreachable")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("download_http_%d", res.StatusCode)
	}
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	var output io.Writer = f
	if len(progress) > 0 {
		output = &progressWriter{writer: f, progress: progress[0]}
	}
	n, err := io.Copy(output, io.LimitReader(res.Body, limit+1))
	if err == nil && n > limit {
		err = fmt.Errorf("download_too_large")
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	return err
}
func checksumFile(file string) (map[string]string, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	if len(b) > 16384 {
		return nil, fmt.Errorf("invalid_checksums")
	}
	out := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		values := strings.Fields(line)
		if len(values) != 2 || len(values[0]) != 64 {
			return nil, fmt.Errorf("invalid_checksums")
		}
		if _, err = hex.DecodeString(values[0]); err != nil {
			return nil, fmt.Errorf("invalid_checksums")
		}
		name := strings.TrimPrefix(values[1], "*")
		if _, ok := out[name]; ok {
			return nil, fmt.Errorf("duplicate_checksum")
		}
		out[name] = values[0]
	}
	return out, nil
}
func verifyFile(file, digest string) error {
	if len(digest) != 64 {
		return fmt.Errorf("missing_checksum")
	}
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	hash := sha256.New()
	if _, err = io.Copy(hash, f); err != nil {
		return err
	}
	if hex.EncodeToString(hash.Sum(nil)) != digest {
		return fmt.Errorf("checksum_mismatch")
	}
	return nil
}
func extractBinaries(file, dest, arch string) error {
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	reader := tar.NewReader(io.LimitReader(gz, 512<<20))
	prefix := "spoof-tunnel-linux-" + arch + "/"
	found := map[string]bool{}
	members := 0
	for {
		header, e := reader.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return e
		}
		members++
		if members > 2048 {
			return fmt.Errorf("too_many_archive_members")
		}
		name := header.Name
		if path.IsAbs(name) || strings.Contains(name, "\\") || path.Clean(name) != strings.TrimSuffix(name, "/") || !strings.HasPrefix(name, prefix) {
			return fmt.Errorf("unsafe_archive_path")
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeDir {
			return fmt.Errorf("unsupported_archive_entry")
		}
		relative := strings.TrimPrefix(name, prefix)
		if relative != "spoof-panel" && relative != "spoof-l3" && relative != "SHA256SUMS" {
			continue
		}
		if found[relative] || header.Typeflag != tar.TypeReg || header.Size <= 0 || header.Size > 128<<20 {
			return fmt.Errorf("invalid_archive_binary")
		}
		found[relative] = true
		target := relative
		if relative == "SHA256SUMS" {
			target = "package-checksums"
			if header.Size > 16384 {
				return fmt.Errorf("invalid_checksums")
			}
		}
		out, e := os.OpenFile(filepath.Join(dest, target), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return e
		}
		_, e = io.CopyN(out, reader, header.Size)
		if e == nil {
			e = out.Sync()
		}
		closeErr := out.Close()
		if e == nil {
			e = closeErr
		}
		if e != nil {
			return e
		}
	}
	if len(found) != 3 {
		return fmt.Errorf("missing_package_binaries")
	}
	return nil
}
func replaceFile(source, dest string, mode os.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	f, err := os.CreateTemp(filepath.Dir(dest), ".replace-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(mode); err == nil {
		_, err = io.Copy(f, input)
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
	if err = os.Rename(name, dest); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(dest))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
func backupDatabase(source, dest string) (string, error) {
	database, err := sql.Open("sqlite", source)
	if err != nil {
		return "", err
	}
	defer database.Close()
	database.SetMaxOpenConns(1)
	if _, err = database.Exec("PRAGMA synchronous=FULL"); err != nil {
		return "", err
	}
	if _, err = database.Exec("VACUUM INTO ?", dest); err != nil {
		return "", err
	}
	if err = os.Chmod(dest, 0600); err != nil {
		return "", err
	}
	var port, prefix string
	if err = database.QueryRow("SELECT value FROM settings WHERE key = 'panel_port'").Scan(&port); err != nil {
		return "", err
	}
	if err = database.QueryRow("SELECT value FROM settings WHERE key = 'web_path'").Scan(&prefix); err != nil {
		return "", err
	}
	n, e := strconv.Atoi(port)
	if e != nil || n < 1 || n > 65535 || prefix == "" || strings.ContainsAny(prefix, "/?#\\\r\n") {
		return "", fmt.Errorf("invalid_panel_endpoint")
	}
	return "http://127.0.0.1:" + strconv.Itoa(n) + "/" + prefix + "/api/health", nil
}
func (u *Updater) waitHealth(ctx context.Context, url, version string) error {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	client := &http.Client{Timeout: 2 * time.Second}
	for {
		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return err
		}
		response, err := client.Do(req)
		if err == nil {
			var health struct {
				Version string `json:"version"`
			}
			decodeErr := json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&health)
			response.Body.Close()
			if response.StatusCode == 200 && decodeErr == nil && health.Version == version {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("updated_panel_did_not_start")
		case <-time.After(500 * time.Millisecond):
		}
	}
}

type progressWriter struct {
	writer   io.Writer
	progress func(int64) error
	written  int64
}

func (p *progressWriter) Write(data []byte) (int, error) {
	n, err := p.writer.Write(data)
	p.written += int64(n)
	if err == nil {
		err = p.progress(p.written)
	}
	return n, err
}
