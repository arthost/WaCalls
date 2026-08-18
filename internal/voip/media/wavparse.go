package media

import (
	"encoding/binary"
	"fmt"
)

// WavInfo is the format of a WAV file that ParseWavStrict accepted, together with
// its interleaved samples.
type WavInfo struct {
	Channels   int
	SampleRate int
	BitDepth   int
	Samples    []float32
}

// ParseWavStrict decodes a 16-bit PCM WAV by walking its RIFF chunks, and rejects
// anything it cannot fully account for. ReadWavFloat32FromBytes is deliberately
// lenient — it scans for a data chunk and otherwise skips a fixed 44-byte header —
// so a slightly unusual but playable file still works. That leniency makes it the
// wrong tool for validating an upload: it reinterprets any blob over 44 bytes as
// noise instead of failing, so a JPEG would be accepted and then played into a
// customer's call.
func ParseWavStrict(raw []byte) (WavInfo, error) {
	var info WavInfo
	if len(raw) < 12 || string(raw[0:4]) != "RIFF" || string(raw[8:12]) != "WAVE" {
		return info, fmt.Errorf("not a RIFF/WAVE file")
	}
	var data []byte
	haveFmt := false
	for off := 12; off+8 <= len(raw); {
		id := string(raw[off : off+4])
		body := off + 8
		// Read as uint64 so a bogus 4 GiB chunk size cannot overflow into a
		// negative int and slip past the bounds check on a 32-bit build.
		size64 := uint64(binary.LittleEndian.Uint32(raw[off+4 : off+8]))
		if uint64(body)+size64 > uint64(len(raw)) {
			return info, fmt.Errorf("chunk %q claims %d bytes but only %d remain", id, size64, len(raw)-body)
		}
		size := int(size64)
		switch id {
		case "fmt ":
			if size < 16 {
				return info, fmt.Errorf("fmt chunk is %d bytes, want at least 16", size)
			}
			format := binary.LittleEndian.Uint16(raw[body : body+2])
			// 0xFFFE is WAVE_FORMAT_EXTENSIBLE, which names its real codec in the
			// extension block instead of here; accept it only when that is PCM.
			if format == wavFormatExtensible && size >= 40 {
				format = binary.LittleEndian.Uint16(raw[body+24 : body+26])
			}
			if format != wavFormatPCM {
				return info, fmt.Errorf("audio format %d is not uncompressed PCM", format)
			}
			info.Channels = int(binary.LittleEndian.Uint16(raw[body+2 : body+4]))
			info.SampleRate = int(binary.LittleEndian.Uint32(raw[body+4 : body+8]))
			info.BitDepth = int(binary.LittleEndian.Uint16(raw[body+14 : body+16]))
			haveFmt = true
		case "data":
			data = raw[body : body+size]
		}
		off = body + size
		if size%2 == 1 {
			// RIFF chunks are word-aligned: an odd-sized body is followed by a pad byte.
			off++
		}
	}
	if !haveFmt {
		return info, fmt.Errorf("no fmt chunk")
	}
	if info.BitDepth != 16 {
		return info, fmt.Errorf("bit depth %d is not supported, want 16", info.BitDepth)
	}
	if info.Channels < 1 || info.Channels > 2 {
		return info, fmt.Errorf("channel count %d is not supported, want mono or stereo", info.Channels)
	}
	if info.SampleRate < 8000 || info.SampleRate > 48000 {
		return info, fmt.Errorf("sample rate %d Hz is outside the supported 8000-48000 Hz range", info.SampleRate)
	}
	if len(data) < 2*info.Channels {
		return info, fmt.Errorf("data chunk holds no samples")
	}
	info.Samples = PCMInt16LEToFloat32(data)
	return info, nil
}

const (
	wavFormatPCM        = 1
	wavFormatExtensible = 0xFFFE
)

// Mono16k downmixes to one channel and resamples to recSampleRate, the 16 kHz mono
// grid every audio path in this service runs on. Normalising once at upload time
// means the hold-music player never has to care what the admin actually uploaded.
func (w WavInfo) Mono16k() []float32 {
	mono := w.Samples
	if w.Channels > 1 {
		mono = downmix(mono, w.Channels)
	}
	if w.SampleRate == recSampleRate {
		return mono
	}
	if w.SampleRate > recSampleRate {
		// Point-sampling 44.1 kHz down to 16 kHz folds everything above 8 kHz back
		// into the audible band as aliasing. A box average over the decimation
		// window is a crude but effective low-pass to run first.
		mono = boxAverage(mono, w.SampleRate/recSampleRate)
	}
	return resampleLinear(mono, w.SampleRate, recSampleRate)
}

// downmix averages interleaved channels into one.
func downmix(in []float32, channels int) []float32 {
	if channels <= 1 {
		return in
	}
	out := make([]float32, len(in)/channels)
	for i := range out {
		var sum float32
		for c := 0; c < channels; c++ {
			sum += in[i*channels+c]
		}
		out[i] = sum / float32(channels)
	}
	return out
}

// boxAverage smooths in with a moving average of the given width.
func boxAverage(in []float32, width int) []float32 {
	if width < 2 || len(in) == 0 {
		return in
	}
	out := make([]float32, len(in))
	for i := range in {
		var sum float32
		n := 0
		for j := i; j < i+width && j < len(in); j++ {
			sum += in[j]
			n++
		}
		out[i] = sum / float32(n)
	}
	return out
}

// resampleLinear rescales in from srcRate to dstRate by linear interpolation.
func resampleLinear(in []float32, srcRate, dstRate int) []float32 {
	if srcRate == dstRate || len(in) == 0 || srcRate <= 0 || dstRate <= 0 {
		return in
	}
	outLen := int(int64(len(in)) * int64(dstRate) / int64(srcRate))
	if outLen <= 0 {
		return nil
	}
	out := make([]float32, outLen)
	ratio := float64(srcRate) / float64(dstRate)
	for i := range out {
		pos := float64(i) * ratio
		j := int(pos)
		if j+1 >= len(in) {
			out[i] = in[len(in)-1]
			continue
		}
		frac := float32(pos - float64(j))
		out[i] = in[j]*(1-frac) + in[j+1]*frac
	}
	return out
}

// EncodeWav16kMono wraps 16 kHz mono float32 samples in a canonical 44-byte PCM
// WAV header.
func EncodeWav16kMono(pcm []float32) []byte {
	body := PCMFloat32ToInt16LE(pcm)
	h := wavHeader16kMono(uint32(len(body)))
	out := make([]byte, 0, len(h)+len(body))
	out = append(out, h[:]...)
	return append(out, body...)
}
