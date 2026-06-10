//go:build !linux

package handlers

func diskAvailableBytes(path string) (int64, bool) {
	return 1 << 62, true
}
