//go:build !linux

package updater

import (
	"context"
	"fmt"
)

func (u *Updater) Apply(context.Context) error { return fmt.Errorf("requires_linux_root") }
func (u *Updater) waitHealth(context.Context, string, string) error {
	return fmt.Errorf("requires_linux_root")
}
