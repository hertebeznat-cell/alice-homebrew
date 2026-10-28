//go:build !linux

package homebrew

import "errors"

func openNativeRing() (ringBackend, error) {
	return nil, errors.New("hardware LED access requires Linux on the speaker")
}
func availableBytes(dir string) uint64 { return ^uint64(0) }
