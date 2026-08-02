package assets

import (
	_ "embed"
	"wacalls/internal/voip/media"
)

//go:embed hold-music.wav
var defaultHoldMusicWAV []byte

// LoadHoldMusic returns 16 kHz mono float32 PCM samples. If path exists, it loads
// from disk (admin custom upload); otherwise it falls back to defaultHoldMusicWAV.
func LoadHoldMusic(diskPath string) []float32 {
	if pcm, err := media.ReadWavFloat32(diskPath); err == nil && len(pcm) > 0 {
		return pcm
	}
	pcm, err := media.ReadWavFloat32FromBytes(defaultHoldMusicWAV)
	if err == nil && len(pcm) > 0 {
		return pcm
	}
	// Fallback to generated wav
	pcm, _ = media.ReadWavFloat32FromBytes(GenerateDefaultHoldMusicWAV())
	return pcm
}
