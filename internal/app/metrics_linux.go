//go:build linux

package app

import "syscall"

func readHostDisk(path string) (totalGB, usedGB, freeGB, percent float64) {
	var stat syscall.Statfs_t
	if path == "" {
		path = "."
	}
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, 0, 0, 0
	}

	totalBytes := stat.Blocks * uint64(stat.Bsize)
	freeBytes := stat.Bavail * uint64(stat.Bsize)
	if totalBytes > 0 {
		usedBytes := totalBytes - freeBytes
		totalGB = float64(totalBytes) / (1024 * 1024 * 1024)
		freeGB = float64(freeBytes) / (1024 * 1024 * 1024)
		usedGB = float64(usedBytes) / (1024 * 1024 * 1024)
		percent = (usedGB / totalGB) * 100.0
	}
	return
}
