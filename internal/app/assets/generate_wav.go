package assets

import (
	"math"
	"os"
	"path/filepath"

	"wacalls/internal/voip/media"
)

func GenerateDefaultHoldMusicWAV() []byte {
	const sampleRate = 16000
	const durationSec = 3.0
	const totalSamples = int(sampleRate * durationSec)

	pcm := make([]float32, totalSamples)
	for i := 0; i < totalSamples; i++ {
		t := float64(i) / float64(sampleRate)
		env := 0.25 * (1.0 + 0.3*math.Sin(2*math.Pi*2.0*t))
		s1 := math.Sin(2 * math.Pi * 440.0 * t)
		s2 := math.Sin(2 * math.Pi * 554.37 * t)
		pcm[i] = float32((s1 + s2) * 0.4 * env)
	}
	// EncodeWav16kMono clamps out-of-range samples for us, so the two summed sines
	// need no manual limiter here.
	return media.EncodeWav16kMono(pcm)
}

func EnsureHoldMusicFile(dir string) error {
	path := filepath.Join(dir, "hold-music.wav")
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	data := GenerateDefaultHoldMusicWAV()
	return os.WriteFile(path, data, 0644)
}
