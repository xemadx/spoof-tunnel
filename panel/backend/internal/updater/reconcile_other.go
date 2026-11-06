//go:build !linux

package updater

// Reconcile is a Linux-only recovery path; other platforms have no
// updater worker and therefore no interrupted-update markers.
func (u *Updater) Reconcile() (bool, error) { return false, nil }
func (u *Updater) hasPendingIntent() bool   { return false }
