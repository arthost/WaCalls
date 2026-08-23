package assets

import (
	"wacalls/internal/voip/media"
)

// LoadHoldMusic returns 16 kHz mono float32 PCM samples if a custom file is uploaded.
// If no file exists on disk, it returns nil (pure silence / no synthetic buzzing noise).
func LoadHoldMusic(diskPath string) []float32 {
	if pcm, err := media.ReadWavFloat32(diskPath); err == nil && len(pcm) > 0 {
		return pcm
	}
	return nil
}
