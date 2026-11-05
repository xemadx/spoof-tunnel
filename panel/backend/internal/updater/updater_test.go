package updater

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func fixtureRelease() Release {
	tag := "v0.4.2"
	base := "https://github.com/xemadx/spoof-tunnel/releases/"
	name := "spoof-tunnel-linux-" + runtime.GOARCH + ".tar.gz"
	return Release{Tag: tag, URL: base + "tag/" + tag, Assets: []Asset{{Name: name, URL: base + "download/" + tag + "/" + name, Size: 1024}, {Name: "SHA256SUMS", URL: base + "download/" + tag + "/SHA256SUMS", Size: 196}}}
}
func TestVersionAndReleaseValidation(t *testing.T) {
	for _, pair := range [][2]string{{"v0.4.2", "v0.4.1"}, {"v1.0.0", "v0.9.99"}, {"v0.10.0", "v0.9.0"}} {
		if !newer(pair[0], pair[1]) || newer(pair[1], pair[0]) {
			t.Fatal(pair)
		}
	}
	for _, invalid := range []string{"v0.4.1", "dev", "v0.4.2;reboot", "v0.4.2/../", "v0.4.2-rc1"} {
		if newer(invalid, "v0.4.1") {
			t.Fatal("invalid upgrade", invalid)
		}
	}
	u := New("v0.4.1", t.TempDir())
	r := fixtureRelease()
	if _, _, err := u.assets(&r); err != nil {
		t.Fatal(err)
	}
	r.Assets[0].URL = "https://evil.example/package"
	if _, _, err := u.assets(&r); err == nil {
		t.Fatal("accepted external asset")
	}
	for _, raw := range []string{"http://github.com/x", "https://github.com.evil/x", "https://user:pass@github.com/x", "https://github.com:8443/x"} {
		v, _ := url.Parse(raw)
		if u.client.CheckRedirect(&http.Request{URL: v}, nil) == nil {
			t.Fatal("unsafe redirect", raw)
		}
	}
}
func TestUpdateStartIsSerializedAndPersistsAcrossPanelRestart(t *testing.T) {
	release := fixtureRelease()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { json.NewEncoder(w).Encode(release) }))
	defer server.Close()
	u := New("v0.4.1", t.TempDir())
	u.apiBase = server.URL
	u.allowed = func() (bool, string) { return true, "" }
	calls := 0
	u.command = func(ctx context.Context, args ...string) error {
		calls++
		if strings.Join(args, " ") != "start --no-block "+Unit {
			t.Fatal("arbitrary service command", args)
		}
		return nil
	}
	if _, err := u.Start(context.Background(), "v0.4.0"); err == nil || calls != 0 {
		t.Fatal("accepted downgrade")
	}
	status, err := u.Start(context.Background(), release.Tag)
	if err != nil {
		t.Fatal(err)
	}
	if status.Job == nil || status.Job.Phase != "queued" || calls != 1 {
		t.Fatal(status)
	}
	if _, err = u.Start(context.Background(), release.Tag); err == nil || calls != 1 {
		t.Fatal("duplicate update started")
	}
	next := New("v0.4.2", u.dataDir)
	if restored := next.Status(); restored.Job == nil || restored.Job.ID != status.Job.ID || restored.Current != "v0.4.2" {
		t.Fatal("lost persistent status", restored)
	}
	info, _ := os.Stat(filepath.Join(u.dir(), "job.json"))
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatal("public update state")
	}
}
func TestLongRunningOneshotIsNotReportedAsStopped(t *testing.T) {
	u := New("v0.4.1", t.TempDir())
	os.MkdirAll(u.dir(), 0700)
	job := Job{ID: strings.Repeat("a", 32), Phase: "downloading", UpdatedAt: time.Now().Add(-time.Minute)}
	atomicJSON(filepath.Join(u.dir(), "job.json"), job)
	u.workerActive = func(context.Context) bool { return true }
	if u.Status().Job.Phase != "downloading" {
		t.Fatal("activating oneshot falsely failed")
	}
	u.workerActive = func(context.Context) bool { return false }
	if u.Status().Job.Phase != "failed" {
		t.Fatal("stopped worker not reported")
	}
}
func TestGithubFailureDoesNotQueueAnUpdate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "rate limited", 403) }))
	defer server.Close()
	u := New("v0.4.1", t.TempDir())
	u.apiBase = server.URL
	if _, err := u.Check(context.Background()); err == nil || err.Error() != fmt.Sprint("github_http_403") {
		t.Fatal(err)
	}
	if u.Status().Job != nil {
		t.Fatal("failed check queued an install")
	}
}
