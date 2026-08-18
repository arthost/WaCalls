package media

import (
	"encoding/binary"
	"math"
	"testing"
)

// buildWav assembles a PCM WAV with the given format, so tests can hand the parser
// files no encoder on this machine would produce.
func buildWav(channels, sampleRate, bitDepth int, samples []int16) []byte {
	body := make([]byte, len(samples)*2)
	for i, s := range samples {
		binary.LittleEndian.PutUint16(body[i*2:], uint16(s))
	}
	out := make([]byte, 0, 44+len(body))
	hdr := make([]byte, 44)
	copy(hdr[0:], "RIFF")
	binary.LittleEndian.PutUint32(hdr[4:], uint32(36+len(body)))
	copy(hdr[8:], "WAVE")
	copy(hdr[12:], "fmt ")
	binary.LittleEndian.PutUint32(hdr[16:], 16)
	binary.LittleEndian.PutUint16(hdr[20:], wavFormatPCM)
	binary.LittleEndian.PutUint16(hdr[22:], uint16(channels))
	binary.LittleEndian.PutUint32(hdr[24:], uint32(sampleRate))
	binary.LittleEndian.PutUint32(hdr[28:], uint32(sampleRate*channels*bitDepth/8))
	binary.LittleEndian.PutUint16(hdr[32:], uint16(channels*bitDepth/8))
	binary.LittleEndian.PutUint16(hdr[34:], uint16(bitDepth))
	copy(hdr[36:], "data")
	binary.LittleEndian.PutUint32(hdr[40:], uint32(len(body)))
	out = append(out, hdr...)
	return append(out, body...)
}

func TestParseWavStrictAcceptsMono16k(t *testing.T) {
	want := []int16{0, 1000, -1000, 32767, -32768}
	info, err := ParseWavStrict(buildWav(1, 16000, 16, want))
	if err != nil {
		t.Fatalf("mono 16 kHz WAV must parse: %v", err)
	}
	if info.Channels != 1 || info.SampleRate != 16000 || info.BitDepth != 16 {
		t.Fatalf("format = %dch/%dHz/%dbit, want 1/16000/16", info.Channels, info.SampleRate, info.BitDepth)
	}
	if len(info.Samples) != len(want) {
		t.Fatalf("got %d samples, want %d", len(info.Samples), len(want))
	}
}

func TestParseWavStrictRejectsNonWav(t *testing.T) {
	// A blob big enough to satisfy ReadWavFloat32FromBytes' fixed 44-byte skip.
	junk := make([]byte, 4096)
	copy(junk, "\xff\xd8\xff\xe0JFIF")
	for i := 16; i < len(junk); i++ {
		junk[i] = byte(i)
	}
	if lenient, err := ReadWavFloat32FromBytes(junk); err != nil || len(lenient) == 0 {
		t.Fatal("precondition: the lenient reader is supposed to accept this junk")
	}
	if _, err := ParseWavStrict(junk); err == nil {
		t.Fatal("a JPEG must not pass as a WAV file")
	}
}

func TestParseWavStrictRejectsBadFormats(t *testing.T) {
	pcm := make([]int16, 320)
	cases := []struct {
		name string
		raw  []byte
	}{
		{"8-bit", buildWav(1, 16000, 8, pcm)},
		{"5.1 channels", buildWav(6, 16000, 16, pcm)},
		{"96 kHz", buildWav(1, 96000, 16, pcm)},
		{"empty data chunk", buildWav(1, 16000, 16, nil)},
		{"truncated", buildWav(1, 16000, 16, pcm)[:8]},
	}
	for _, tc := range cases {
		if _, err := ParseWavStrict(tc.raw); err == nil {
			t.Errorf("%s must be rejected", tc.name)
		}
	}
}

func TestParseWavStrictRejectsOversizedChunk(t *testing.T) {
	raw := buildWav(1, 16000, 16, make([]int16, 320))
	// Claim the data chunk holds far more than the file actually carries; a parser
	// that trusted this would read past the buffer.
	binary.LittleEndian.PutUint32(raw[40:], 1<<30)
	if _, err := ParseWavStrict(raw); err == nil {
		t.Fatal("a data chunk longer than the file must be rejected")
	}
}

func TestParseWavStrictSkipsExtraChunks(t *testing.T) {
	// Real encoders interleave LIST/INFO metadata between fmt and data, sometimes
	// with an odd length that needs the RIFF pad byte to be honoured.
	base := buildWav(1, 16000, 16, []int16{100, 200, 300})
	var raw []byte
	raw = append(raw, base[:36]...) // RIFF + WAVE + fmt chunk
	list := []byte("LIST")
	list = binary.LittleEndian.AppendUint32(list, 5)
	list = append(list, []byte("INFO\x00")...) // 5 bytes: odd, so a pad byte follows
	list = append(list, 0)
	raw = append(raw, list...)
	raw = append(raw, base[36:]...) // data chunk
	binary.LittleEndian.PutUint32(raw[4:], uint32(len(raw)-8))

	info, err := ParseWavStrict(raw)
	if err != nil {
		t.Fatalf("a WAV with a LIST chunk must parse: %v", err)
	}
	if len(info.Samples) != 3 {
		t.Fatalf("got %d samples, want 3 — chunk walking lost the data chunk", len(info.Samples))
	}
}

func TestMono16kDownmixesAndResamples(t *testing.T) {
	// 1 s of 48 kHz stereo: left rail high, right rail low, so a correct downmix
	// lands near the midpoint instead of either rail.
	const srcRate = 48000
	samples := make([]int16, srcRate*2)
	for i := 0; i < srcRate; i++ {
		samples[i*2] = 16000
		samples[i*2+1] = -8000
	}
	info, err := ParseWavStrict(buildWav(2, srcRate, 16, samples))
	if err != nil {
		t.Fatalf("stereo 48 kHz WAV must parse: %v", err)
	}
	out := info.Mono16k()

	// 1 s at 48 kHz stereo becomes 1 s at 16 kHz mono; allow a sample of slack for
	// the resampler's integer truncation.
	if diff := len(out) - recSampleRate; diff < -2 || diff > 2 {
		t.Fatalf("got %d samples, want ~%d (1 s at 16 kHz)", len(out), recSampleRate)
	}
	wantLevel := float32(16000-8000) / 2 / 32768
	mid := out[len(out)/2]
	if math.Abs(float64(mid-wantLevel)) > 0.01 {
		t.Fatalf("downmixed level = %f, want ~%f", mid, wantLevel)
	}
}

func TestMono16kPassesThroughNativeFormat(t *testing.T) {
	want := []int16{0, 500, -500, 1200}
	info, err := ParseWavStrict(buildWav(1, 16000, 16, want))
	if err != nil {
		t.Fatal(err)
	}
	out := info.Mono16k()
	if len(out) != len(want) {
		t.Fatalf("16 kHz mono must not be resampled: got %d samples, want %d", len(out), len(want))
	}
}

func TestEncodeWav16kMonoRoundTrips(t *testing.T) {
	in := []float32{0, 0.5, -0.5, 0.25}
	info, err := ParseWavStrict(EncodeWav16kMono(in))
	if err != nil {
		t.Fatalf("our own encoder must satisfy our own parser: %v", err)
	}
	if info.Channels != 1 || info.SampleRate != recSampleRate || info.BitDepth != 16 {
		t.Fatalf("format = %dch/%dHz/%dbit, want 1/%d/16", info.Channels, info.SampleRate, info.BitDepth, recSampleRate)
	}
	if len(info.Samples) != len(in) {
		t.Fatalf("got %d samples, want %d", len(info.Samples), len(in))
	}
	for i := range in {
		if math.Abs(float64(info.Samples[i]-in[i])) > 0.001 {
			t.Errorf("sample %d = %f, want %f", i, info.Samples[i], in[i])
		}
	}
}
