//go:build linux

package updater

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func digest(b []byte) string                                              { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
func makePackage(t *testing.T, binary []byte, unsafe bool) []byte {
	t.Helper()
	var buffer bytes.Buffer
	gz := gzip.NewWriter(&buffer)
	archive := tar.NewWriter(gz)
	files := map[string][]byte{"spoof-panel": binary, "spoof-l3": binary, "SHA256SUMS": []byte(digest(binary) + "  spoof-panel\n" + digest(binary) + "  spoof-l3\n")}
	if unsafe {
		files["../escape"] = []byte("no")
	}
	for name, content := range files {
		h := &tar.Header{Name: "spoof-tunnel-linux-" + runtime.GOARCH + "/" + name, Mode: 0755, Size: int64(len(content))}
		if err := archive.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		archive.Write(content)
	}
	archive.Close()
	gz.Close()
	return buffer.Bytes()
}
func TestVerifiedUpdateAndRollbackWithRealPanelProcesses(t *testing.T) {
	binaries := t.TempDir()
	oldPath, newPath := filepath.Join(binaries, "old"), filepath.Join(binaries, "new")
	for _, item := range [][2]string{{oldPath, "v0.4.1"}, {newPath, "v0.4.2"}} {
		cmd := exec.Command("go", "build", "-o", item[0], "-ldflags", "-X main.version="+item[1], "testdata/panel.go")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatal(err, string(output))
		}
	}
	oldBytes, _ := os.ReadFile(oldPath)
	newBytes, _ := os.ReadFile(newPath)
	for _, scenario := range []string{"success", "rollback", "bad-checksum", "unsafe-archive", "wrong-version", "cancel-after-stop", "stop-command-error"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			u := New("v0.4.1", dir)
			u.panelPath = filepath.Join(dir, "spoof-panel")
			u.corePath = filepath.Join(dir, "spoof-l3")
			u.allowed = func() (bool, string) { return true, "" }
			os.WriteFile(u.panelPath, oldBytes, 0755)
			os.WriteFile(u.corePath, oldBytes, 0755)
			socket, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			port := fmt.Sprint(socket.Addr().(*net.TCPAddr).Port)
			socket.Close()
			env := append(os.Environ(), "FIXTURE_DATA="+dir, "FIXTURE_PORT="+port)
			seed := exec.Command(u.panelPath, "--seed")
			seed.Env = env
			if out, err := seed.CombinedOutput(); err != nil {
				t.Fatal(err, string(out))
			}
			var process *exec.Cmd
			stops := 0
			start := func() error { process = exec.Command(u.panelPath); process.Env = env; return process.Start() }
			stop := func() error {
				if process == nil {
					return nil
				}
				_ = process.Process.Kill()
				_ = process.Wait()
				process = nil
				return nil
			}
			defer stop()
			if err = start(); err != nil {
				t.Fatal(err)
			}
			healthURL := "http://127.0.0.1:" + port + "/fixture/api/health"
			if err = u.waitHealth(context.Background(), healthURL, "v0.4.1"); err != nil {
				t.Fatal(err)
			}
			var token string
			login, _ := http.Post("http://127.0.0.1:"+port+"/fixture/api/auth/login", "application/json", strings.NewReader(`{"username":"update-test","password":"test-only-update-password"}`))
			var credentials struct {
				Token string `json:"token"`
			}
			json.NewDecoder(login.Body).Decode(&credentials)
			login.Body.Close()
			token = credentials.Token
			if token == "" {
				t.Fatal("fixture login failed")
			}
			packageBytes := newBytes
			if scenario == "wrong-version" {
				packageBytes = oldBytes
			}
			archive := makePackage(t, packageBytes, scenario == "unsafe-archive")
			sum := digest(archive)
			if scenario == "bad-checksum" {
				sum = strings.Repeat("0", 64)
			}
			name := "spoof-tunnel-linux-" + runtime.GOARCH + ".tar.gz"
			sums := []byte(sum + "  " + name + "\n")
			release := fixtureRelease()
			release.Assets[0].Size = int64(len(archive))
			release.Assets[1].Size = int64(len(sums))
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasSuffix(r.URL.Path, "/"+name):
					w.Write(archive)
				case strings.HasSuffix(r.URL.Path, "/SHA256SUMS"):
					w.Write(sums)
				default:
					json.NewEncoder(w).Encode(release)
				}
			}))
			defer server.Close()
			u.apiBase = server.URL
			local, _ := url.Parse(server.URL)
			u.client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
				copy := r.Clone(r.Context())
				copy.URL = new(url.URL)
				*copy.URL = *r.URL
				if copy.URL.Host == "github.com" {
					copy.URL.Scheme = local.Scheme
					copy.URL.Host = local.Host
				}
				return http.DefaultTransport.RoundTrip(copy)
			})
			applyCtx, cancelApply := context.WithCancel(context.Background())
			defer cancelApply()
			u.command = func(ctx context.Context, args ...string) error {
				if err := ctx.Err(); err != nil {
					return err
				}
				if args[len(args)-1] == Unit {
					return nil
				}
				if strings.Join(args, " ") == "stop spoof-panel.service" {
					stops++
					err := stop()
					if scenario == "cancel-after-stop" {
						cancelApply()
					}
					if scenario == "stop-command-error" {
						return fmt.Errorf("injected_stop_result_error")
					}
					return err
				}
				if strings.Join(args, " ") == "start spoof-panel.service" {
					return start()
				}
				return fmt.Errorf("unexpected service operation: %v", args)
			}
			if scenario == "rollback" {
				u.health = func(ctx context.Context, url, version string) error {
					if version == "v0.4.2" {
						database, _ := sql.Open("sqlite", filepath.Join(dir, "panel.db"))
						_, err := database.Exec("UPDATE settings SET value='broken-new-state' WHERE key='jwt_secret'")
						database.Close()
						if err != nil {
							t.Fatal(err)
						}
						return fmt.Errorf("injected_startup_failure")
					}
					return u.waitHealth(ctx, url, version)
				}
			}
			if _, err = u.Start(context.Background(), release.Tag); err != nil {
				t.Fatal(err)
			}
			err = u.Apply(applyCtx)
			if scenario == "success" && err != nil {
				t.Fatal(err)
			}
			if scenario != "success" && err == nil {
				t.Fatal("fault injection did not fail")
			}
			status := u.Status()
			wantPhase := "failed"
			wantVersion := "v0.4.1"
			if scenario == "success" {
				wantPhase = "succeeded"
				wantVersion = "v0.4.2"
			} else if scenario == "rollback" || scenario == "cancel-after-stop" || scenario == "stop-command-error" {
				wantPhase = "rolled_back"
			}
			if scenario == "success" && (status.Job.BytesDone != int64(len(archive)) || status.Job.BytesTotal != int64(len(archive)) || status.Job.Progress != 100) {
				t.Fatal("progress did not reflect downloaded bytes", status.Job)
			}
			if status.Job.Phase != wantPhase {
				t.Fatal("wrong update outcome", status.Job, err)
			}
			if scenario != "success" && scenario != "rollback" && scenario != "cancel-after-stop" && scenario != "stop-command-error" && stops != 0 {
				t.Fatal("invalid package stopped the panel")
			}
			if err = u.waitHealth(context.Background(), healthURL, wantVersion); err != nil {
				t.Fatal(err)
			}
			req, _ := http.NewRequest("GET", "http://127.0.0.1:"+port+"/fixture/api/auth/me", nil)
			req.Header.Set("Authorization", "Bearer "+token)
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			res.Body.Close()
			if res.StatusCode != 200 {
				t.Fatal("admin session lost after update/rollback")
			}
			database, _ := sql.Open("sqlite", filepath.Join(dir, "panel.db"))
			defer database.Close()
			var secret string
			database.QueryRow("SELECT value FROM settings WHERE key='jwt_secret'").Scan(&secret)
			var groups int
			database.QueryRow("SELECT count(*) FROM pool_files").Scan(&groups)
			if secret != "isolated-persistent-update-test-secret" || groups != 2 {
				t.Fatal("database contents lost", secret, groups)
			}
			if scenario == "success" || scenario == "rollback" {
				if info, e := os.Stat(filepath.Join(u.dir(), status.Job.ID, "panel-backup.db")); e != nil || info.Mode().Perm() != 0600 {
					t.Fatal("missing private consistent backup", e)
				}
			}
			data, _ := os.ReadFile(u.panelPath)
			expected := oldBytes
			if scenario == "success" {
				expected = newBytes
			}
			if !bytes.Equal(data, expected) {
				t.Fatal("wrong binary installed")
			}
		})
	}
}
func TestBoundedDownloadAndArchivePaths(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, strings.Repeat("x", 128)) }))
	defer server.Close()
	u := New("v0.4.1", t.TempDir())
	if err := u.download(context.Background(), server.URL, filepath.Join(t.TempDir(), "data"), 64); err == nil {
		t.Fatal("unbounded download")
	}
}

func TestDownloadProgressCountsActualBytes(t *testing.T) {
	content := strings.Repeat("x", 256<<10)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, content) }))
	defer server.Close()
	u := New("v0.4.1", t.TempDir())
	var last int64
	calls := 0
	err := u.download(context.Background(), server.URL, filepath.Join(t.TempDir(), "download"), 1<<20, func(n int64) error {
		if n <= last {
			t.Fatal("progress went backwards")
		}
		last = n
		calls++
		return nil
	})
	if err != nil || last != int64(len(content)) || calls < 2 {
		t.Fatal("progress is not based on bytes", last, calls, err)
	}
}

func TestBackupIncludesCommittedWALAndPreservesPrivateMode(t *testing.T) {
	directory := t.TempDir()
	source := filepath.Join(directory, "panel.db")
	backup := filepath.Join(directory, "backup.db")
	writer, err := sql.Open("sqlite", source)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	for _, query := range []string{"PRAGMA journal_mode=WAL", "PRAGMA wal_autocheckpoint=0", "CREATE TABLE settings (key TEXT PRIMARY KEY,value TEXT)", "INSERT INTO settings VALUES ('panel_port','12345'),('web_path','fixture'),('jwt_secret','committed-wal-secret')"} {
		if _, err := writer.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	if info, err := os.Stat(source + "-wal"); err != nil || info.Size() == 0 {
		t.Fatal("fixture has no WAL data")
	}
	if _, err = backupDatabase(source, backup); err != nil {
		t.Fatal(err)
	}
	snapshot, err := sql.Open("sqlite", backup)
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	var secret string
	if err = snapshot.QueryRow("SELECT value FROM settings WHERE key='jwt_secret'").Scan(&secret); err != nil || secret != "committed-wal-secret" {
		t.Fatal("backup omitted committed WAL contents", secret, err)
	}
}
