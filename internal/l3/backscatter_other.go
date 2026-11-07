//go:build !linux

package l3

// BackscatterSuppression is a no-op on non-Linux platforms.
type BackscatterSuppression struct{}

func StartBackscatterSuppression(proto string, port int) *BackscatterSuppression {
	return &BackscatterSuppression{}
}

func (s *BackscatterSuppression) Stop() {}
