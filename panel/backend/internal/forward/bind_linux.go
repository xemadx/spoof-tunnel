//go:build linux

package forward

import "syscall"

func bindTunnelSocket(name string, raw syscall.RawConn) error {
	var socketErr error
	if err := raw.Control(func(fd uintptr) {
		socketErr = syscall.SetsockoptString(int(fd), syscall.SOL_SOCKET, syscall.SO_BINDTODEVICE, name)
	}); err != nil {
		return err
	}
	return socketErr
}
