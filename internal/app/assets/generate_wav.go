package assets

import (
	"bytes"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
)

func GenerateDefaultHoldMusicWAV() []byte {
	const sampleRate = 16000
	const durationSec = 3.0
	const totalSamples = int(sampleRate * durationSec)

	pcm16 := make([]int16, totalSamples)
	for i := 0; i < totalSamples; i++ {
		t := float64(i) / float64(sampleRate)
		env := 0.25 * (1.0 + 0.3*math.Sin(2*math.Pi*2.0*t))
		s1 := math.Sin(2 * math.Pi * 440.0 * t)
		s2 := math.Sin(2 * math.Pi * 554.37 * t)
		sample := (s1 + s2) * 0.4 * env
		if sample > 1.0 {
			sample = 1.0
		} else if sample < -1.0 {
			sample = -1.0
		}
		pcm16[i] = int16(sample * 32767.0)
	}

	buf := new(bytes.Buffer)
	dataLen := uint32(totalSamples * 2)

	buf.WriteString("RIFF")
	_ = binary.Write(buf, binary.LittleEndian, uint32(36+dataLen))
	buf.WriteString("WAVEfmt ")
	_ = binary.Write(buf, binary.LittleEndian, uint32(16))
	_ = binary.Write(buf, binary.LittleEndian, uint16(1))
	_ = binary.Write(buf, binary.LittleEndian, uint16(1))
	_ = binary.Write(buf, binary.LittleEndian, uint32(sampleRate))
	_ = binary.Write(buf, binary.LittleEndian, uint32(sampleRate*2))
	_ = binary.Write(buf, binary.LittleEndian, uint16(2))
	_ = binary.Write(buf, binary.LittleEndian, uint16(16))
	buf.WriteString("data")
	_ = binary.Write(buf, binary.LittleEndian, dataLen)

	for _, s := range pcm16 {
		_ = binary.Write(buf, binary.LittleEndian, s)
	}
	return buf.Bytes()
}

func EnsureHoldMusicFile(dir string) error {
	path := filepath.Join(dir, "hold-music.wav")
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	data := GenerateDefaultHoldMusicWAV()
	return os.WriteFile(path, data, 0644)
}
