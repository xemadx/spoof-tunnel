//go:build !linux

package forward

import (
	"fmt"
	"syscall"
)

func bindTunnelSocket(name string, raw syscall.RawConn) error {
	return fmt.Errorf("TUN-bound forwarding requires Linux")
}
