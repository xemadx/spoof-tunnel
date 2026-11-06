//go:build linux

package updater

import (
	"os"
	"path/filepath"
	"testing"
)

// fakeVersionBinary writes a tiny executable that answers --version with
// the given output.
func fakeVersionBinary(t *testing.T, path, output string) {
	t.Helper()
	script := "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo \"" + output + "\"; exit 0; fi\necho unexpected >&2; exit 1\n"
	if err := os.WriteFile(path, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
}

func setupReconcile(t *testing.T) (*Updater, string) {
	t.Helper()
	dir := t.TempDir()
	u := New("v0.4.1", dir)
	u.panelPath = filepath.Join(dir, "spoof-panel")
	u.corePath = filepath.Join(dir, "spoof-l3")
	return u, dir
}

func writeIntent(t *testing.T, u *Updater, id, from, target string) string {
	t.Helper()
	stage := filepath.Join(u.dir(), id)
	if err := os.MkdirAll(stage, 0700); err != nil {
		t.Fatal(err)
	}
	if err := atomicJSON(filepath.Join(stage, "intent.json"), updateIntent{ID: id, From: from, Target: target}); err != nil {
		t.Fatal(err)
	}
	return stage
}

func TestReconcileNoIntent(t *testing.T) {
	u, _ := setupReconcile(t)
	if restart, err := u.Reconcile(); err != nil || restart {
		t.Fatalf("no intent: restart=%v err=%v", restart, err)
	}
}

func TestReconcileAlreadyCompletedOrUntouched(t *testing.T) {
	for _, pair := range [][2]string{{"v0.4.2", "v0.4.2"}, {"v0.4.1", "v0.4.1"}} {
		u, _ := setupReconcile(t)
		fakeVersionBinary(t, u.panelPath, pair[0])
		fakeVersionBinary(t, u.corePath, "spoof-l3 "+pair[1])
		stage := writeIntent(t, u, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "v0.4.1", "v0.4.2")
		if restart, err := u.Reconcile(); err != nil || restart {
			t.Fatalf("versions %v: restart=%v err=%v", pair, restart, err)
		}
		if _, err := os.Stat(filepath.Join(stage, "intent.json")); !os.IsNotExist(err) {
			t.Fatal("intent was not cleared")
		}
	}
}

func TestReconcileCompletesKilledBetweenRenames(t *testing.T) {
	u, _ := setupReconcile(t)
	// On disk: old panel, new core — the worker died after the core rename.
	fakeVersionBinary(t, u.panelPath, "v0.4.1")
	fakeVersionBinary(t, u.corePath, "spoof-l3 v0.4.2")
	stage := writeIntent(t, u, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "v0.4.1", "v0.4.2")
	// The staged new panel binary survived because the worker never ran
	// its cleanup.
	fakeVersionBinary(t, filepath.Join(stage, "spoof-panel"), "v0.4.2")
	restart, err := u.Reconcile()
	if err != nil || !restart {
		t.Fatalf("restart=%v err=%v", restart, err)
	}
	if version, err := binaryVersion(u.panelPath); err != nil || version != "v0.4.2" {
		t.Fatalf("panel not completed: version=%q err=%v", version, err)
	}
	if _, err := os.Stat(filepath.Join(stage, "intent.json")); !os.IsNotExist(err) {
		t.Fatal("intent was not cleared")
	}
}

func TestReconcileMixedWithoutStagedBinaryRestoresPrevious(t *testing.T) {
	u, _ := setupReconcile(t)
	// New panel, old core, no staged binaries: restore both previous ones.
	fakeVersionBinary(t, u.panelPath, "v0.4.2")
	fakeVersionBinary(t, u.corePath, "spoof-l3 v0.4.1")
	stage := writeIntent(t, u, "cccccccccccccccccccccccccccccccc", "v0.4.1", "v0.4.2")
	fakeVersionBinary(t, filepath.Join(stage, "previous-panel"), "v0.4.1")
	fakeVersionBinary(t, filepath.Join(stage, "previous-core"), "spoof-l3 v0.4.1")
	restart, err := u.Reconcile()
	if err != nil || !restart {
		t.Fatalf("restart=%v err=%v", restart, err)
	}
	if version, err := binaryVersion(u.panelPath); err != nil || version != "v0.4.1" {
		t.Fatalf("panel not restored: version=%q err=%v", version, err)
	}
	if version, err := binaryVersion(u.corePath); err != nil || version != "v0.4.1" {
		t.Fatalf("core not restored: version=%q err=%v", version, err)
	}
}

func TestReconcileMixedWithoutAnyBackupFails(t *testing.T) {
	u, _ := setupReconcile(t)
	fakeVersionBinary(t, u.panelPath, "v0.4.2")
	fakeVersionBinary(t, u.corePath, "spoof-l3 v0.4.1")
	stage := writeIntent(t, u, "dddddddddddddddddddddddddddddddd", "v0.4.1", "v0.4.2")
	restart, err := u.Reconcile()
	if err == nil {
		t.Fatalf("unrecoverable mix accepted: restart=%v", restart)
	}
	if _, err := os.Stat(filepath.Join(stage, "intent.json")); err != nil {
		t.Fatal("intent must survive a failed reconcile for manual recovery")
	}
}
