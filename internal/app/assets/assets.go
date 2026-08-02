package assets

import (
	"wacalls/internal/voip/media"
)

// LoadHoldMusic returns 16 kHz mono float32 PCM samples. If path exists, it loads
// from disk (admin custom upload); otherwise it falls back to GenerateDefaultHoldMusicWAV().
func LoadHoldMusic(diskPath string) []float32 {
	if pcm, err := media.ReadWavFloat32(diskPath); err == nil && len(pcm) > 0 {
		return pcm
	}
	pcm, _ := media.ReadWavFloat32FromBytes(GenerateDefaultHoldMusicWAV())
	return pcm
}
