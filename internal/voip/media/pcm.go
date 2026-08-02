package media

import (
	"encoding/binary"
	"io"
	"math"
	"os"
)

func PCMFloat32ToInt16LE(pcm []float32) []byte {
	out := make([]byte, len(pcm)*2)
	for i, s := range pcm {
		binary.LittleEndian.PutUint16(out[i*2:], uint16(floatToInt16(s)))
	}
	return out
}

func PCMInt16LEToFloat32(b []byte) []float32 {
	out := make([]float32, len(b)/2)
	for i := range out {
		v := int16(binary.LittleEndian.Uint16(b[i*2:]))
		out[i] = float32(v) / 32768.0
	}
	return out
}

func floatToInt16(s float32) int16 {
	switch {
	case math.IsNaN(float64(s)):
		return 0
	case s >= 1:
		return math.MaxInt16
	case s <= -1:
		return math.MinInt16
	}
	return int16(s * 32767)
}

func ReadWavFloat32FromBytes(raw []byte) ([]float32, error) {
	if len(raw) < 44 {
		return nil, io.ErrUnexpectedEOF
	}
	// Look for "data" marker
	idx := 36
	for idx < len(raw)-8 {
		if string(raw[idx:idx+4]) == "data" {
			dataLen := binary.LittleEndian.Uint32(raw[idx+4 : idx+8])
			start := idx + 8
			end := start + int(dataLen)
			if end > len(raw) {
				end = len(raw)
			}
			return PCMInt16LEToFloat32(raw[start:end]), nil
		}
		idx++
	}
	// Fallback skip 44 header bytes
	return PCMInt16LEToFloat32(raw[44:]), nil
}

func ReadWavFloat32(path string) ([]float32, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ReadWavFloat32FromBytes(data)
}
