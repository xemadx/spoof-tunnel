//go:build linux

package benchmark

import "github.com/ParsaKSH/spoof-tunnel/internal/l3"

func RawFactory(c l3.Config, tag []byte) (Wire, error) { return l3.NewTaggedPacketConn(c, tag) }
