//go:build linux

package updater

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Interrupted-update recovery.
//
// Apply replaces the two binaries one after another (core first, then the
// panel). If the worker is killed or the power fails between the two
// renames, or while the new panel is starting, the on-disk pair is left
// mixed and no in-process rollback can run: the deferred rollback in
// Apply only executes while the worker process is alive. Docs are honest
// that power loss is outside the automatic guarantee; this file narrows
// what "outside" means to a single recoverable case.
//
// Every update writes updates/<id>/intent.json before replacing anything
// and removes it once the new pair answers its health check. On panel
// startup, Reconcile inspects any leftover intent:
//
//   - both binaries already report the target version: the update had in
//     fact completed; drop the intent.
//   - both report the previous version: nothing was replaced; drop it.
//   - panel reports the previous version and core the target: the worker
//     died between the two renames. The verified new panel binary is still
//     in the stage directory (the worker's cleanup never ran), so the
//     update is completed from it and the service is restarted.
//   - anything else (unreadable binaries, missing stage files): the
//     previous binaries are restored when present and the service is
//     restarted; if that is impossible the intent is left in place and an
//     error is returned so the operator can recover manually.
//
// Reconcile never touches the database: in the kill-between-renames case
// the database was never replaced (it is only replaced by the in-process
// rollback, which also restores it before the binaries). The database
// backup is restored by that rollback path, whose order is database
// first, so a kill mid-rollback also leaves a consistent database.
type updateIntent struct {
	ID     string `json:"id"`
	From   string `json:"from"`
	Target string `json:"target"`
}

// Reconcile recovers interrupted updates left behind by a dead worker.
// It returns true when the caller should exit so systemd restarts the
// service with the reconciled binaries (the panel unit restarts on
// failure). A nil error with false means there was nothing to do.
func (u *Updater) Reconcile() (bool, error) {
	entries, err := os.ReadDir(u.dir())
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	restart := false
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		stage := filepath.Join(u.dir(), entry.Name())
		intentPath := filepath.Join(stage, "intent.json")
		raw, err := os.ReadFile(intentPath)
		if err != nil {
			continue
		}
		var intent updateIntent
		if json.Unmarshal(raw, &intent) != nil || intent.ID != entry.Name() || !tagPattern.MatchString(intent.Target) || !tagPattern.MatchString(intent.From) {
			continue
		}
		done, err := u.reconcileIntent(stage, intent)
		if err != nil {
			return restart, err
		}
		restart = restart || done
	}
	return restart, nil
}

func (u *Updater) reconcileIntent(stage string, intent updateIntent) (bool, error) {
	panelVersion, panelErr := binaryVersion(u.panelPath)
	coreVersion, coreErr := binaryVersion(u.corePath)
	if panelErr != nil || coreErr != nil {
		return false, fmt.Errorf("interrupted update %s: cannot verify installed versions (panel: %v, core: %v); manual recovery from %s", intent.ID, panelErr, coreErr, stage)
	}
	switch {
	case panelVersion == intent.Target && coreVersion == intent.Target:
		log.Printf("update %s already completed; clearing interrupted-update marker", intent.ID)
	case panelVersion == intent.From && coreVersion == intent.From:
		log.Printf("update %s replaced nothing; clearing interrupted-update marker", intent.ID)
	case panelVersion == intent.From && coreVersion == intent.Target:
		// Killed between the two renames. The staged panel binary was
		// already verified (checksum, ELF, --version) before anything
		// was replaced, so completing the update is safe.
		staged := filepath.Join(stage, "spoof-panel")
		if _, err := os.Stat(staged); err != nil {
			return false, fmt.Errorf("interrupted update %s: staged panel binary missing; manual recovery from %s", intent.ID, stage)
		}
		if err := replaceFile(staged, u.panelPath, 0755); err != nil {
			return false, fmt.Errorf("interrupted update %s: cannot complete panel replacement: %w", intent.ID, err)
		}
		log.Printf("update %s interrupted between binary replacements; completed it, restarting service", intent.ID)
		return true, u.removeIntent(stage)
	default:
		// Any other mixture (including a new panel with an old core):
		// restore both previous binaries when present, then restart so
		// the running process matches the on-disk pair.
		restored := false
		for _, pair := range [][2]string{{"previous-panel", u.panelPath}, {"previous-core", u.corePath}} {
			src := filepath.Join(stage, pair[0])
			if _, err := os.Stat(src); err != nil {
				continue
			}
			if err := replaceFile(src, pair[1], 0755); err != nil {
				return false, fmt.Errorf("interrupted update %s: cannot restore %s: %w", intent.ID, pair[0], err)
			}
			restored = true
		}
		if !restored {
			return false, fmt.Errorf("interrupted update %s: mixed versions %s/%s with no previous binaries; manual recovery from %s", intent.ID, panelVersion, coreVersion, stage)
		}
		log.Printf("update %s left mixed versions %s/%s; restored previous binaries, restarting service", intent.ID, panelVersion, coreVersion)
		return true, u.removeIntent(stage)
	}
	return false, u.removeIntent(stage)
}

func (u *Updater) removeIntent(stage string) error {
	if err := os.Remove(filepath.Join(stage, "intent.json")); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// binaryVersion runs a binary with --version and returns the bare tag:
// "vX.Y.Z" for the panel, "spoof-l3 vX.Y.Z" for the core.
func binaryVersion(path string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "--version")
	cmd.Env = append(os.Environ(), "SPOOF_DATA_DIR="+os.TempDir())
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	version := strings.TrimSpace(string(out))
	version = strings.TrimPrefix(version, "spoof-l3 ")
	if !tagPattern.MatchString(version) {
		return "", fmt.Errorf("unexpected version output %q", version)
	}
	return version, nil
}

// hasPendingIntent reports whether any interrupted-update marker exists.
func (u *Updater) hasPendingIntent() bool {
	entries, err := os.ReadDir(u.dir())
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(u.dir(), entry.Name(), "intent.json")); err == nil {
			return true
		}
	}
	return false
}
