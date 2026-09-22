//go:build !linux

package app

func readHostDisk(path string) (totalGB, usedGB, freeGB, percent float64) {
	return 0, 0, 0, 0
}
