package app

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"log/slog"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"wacalls/internal/app/events"
	"wacalls/internal/app/session"
	"wacalls/internal/voip/media"
)

func holdMusicServer(t *testing.T) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	return &Server{
		authorize: bearerAuthorizer(""),
		broker:    events.NewBroker(nil, slog.Default()),
		sessions:  session.NewManager(session.Deps{}),
		dataDir:   dir,
	}, dir
}

// multipartBody wraps raw bytes in the multipart body the upload handler expects.
func multipartBody(t *testing.T, field, filename string, raw []byte) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile(field, filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf, mw.FormDataContentType()
}

func postHoldMusic(t *testing.T, s *Server, raw []byte) *httptest.ResponseRecorder {
	t.Helper()
	body, ctype := multipartBody(t, "file", "hold.wav", raw)
	req := httptest.NewRequest("POST", "/api/holdmusic", body)
	req.Header.Set("Content-Type", ctype)
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, req)
	return rec
}

func wavFixture(channels, sampleRate int, frames int) []byte {
	if channels == 1 && sampleRate == 16000 {
		pcm := make([]float32, frames)
		for i := range pcm {
			pcm[i] = 0.25
		}
		return media.EncodeWav16kMono(pcm)
	}
	// Only the 16 kHz mono encoder is exported, so hand-roll other formats here to
	// exercise the conversion the handler does on upload.
	body := make([]byte, frames*channels*2)
	for i := 0; i < frames*channels; i++ {
		binary.LittleEndian.PutUint16(body[i*2:], 8192)
	}
	hdr := make([]byte, 44)
	copy(hdr[0:], "RIFF")
	binary.LittleEndian.PutUint32(hdr[4:], uint32(36+len(body)))
	copy(hdr[8:], "WAVE")
	copy(hdr[12:], "fmt ")
	binary.LittleEndian.PutUint32(hdr[16:], 16)
	binary.LittleEndian.PutUint16(hdr[20:], 1) // PCM
	binary.LittleEndian.PutUint16(hdr[22:], uint16(channels))
	binary.LittleEndian.PutUint32(hdr[24:], uint32(sampleRate))
	binary.LittleEndian.PutUint32(hdr[28:], uint32(sampleRate*channels*2))
	binary.LittleEndian.PutUint16(hdr[32:], uint16(channels*2))
	binary.LittleEndian.PutUint16(hdr[34:], 16)
	copy(hdr[36:], "data")
	binary.LittleEndian.PutUint32(hdr[40:], uint32(len(body)))
	return append(hdr, body...)
}

func TestHoldMusicUploadStoresNormalizedFile(t *testing.T) {
	s, dir := holdMusicServer(t)

	// 1 s of 48 kHz stereo: the stored file must come back as 16 kHz mono.
	rec := postHoldMusic(t, s, wavFixture(2, 48000, 48000))
	if rec.Code != 200 {
		t.Fatalf("upload: want 200, got %d %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Status      string `json:"status"`
		Size        int    `json:"size"`
		SourceRate  int    `json:"source_rate"`
		SourceChans int    `json:"source_chans"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Status != "ok" || resp.SourceRate != 48000 || resp.SourceChans != 2 {
		t.Fatalf("response = %+v, want ok/48000/2", resp)
	}

	stored, err := os.ReadFile(filepath.Join(dir, "hold-music.wav"))
	if err != nil {
		t.Fatalf("upload must land in dataDir: %v", err)
	}
	info, err := media.ParseWavStrict(stored)
	if err != nil {
		t.Fatalf("stored file must be a valid WAV: %v", err)
	}
	if info.Channels != 1 || info.SampleRate != 16000 {
		t.Fatalf("stored format = %dch/%dHz, want 1ch/16000Hz — the player assumes 16 kHz mono",
			info.Channels, info.SampleRate)
	}
	if resp.Size != len(stored) {
		t.Errorf("reported size %d, file is %d bytes", resp.Size, len(stored))
	}
}

func TestHoldMusicUploadRejectsNonWav(t *testing.T) {
	s, dir := holdMusicServer(t)
	junk := make([]byte, 4096)
	for i := range junk {
		junk[i] = byte(i)
	}
	rec := postHoldMusic(t, s, junk)
	if rec.Code != 400 {
		t.Fatalf("junk upload: want 400, got %d %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "hold-music.wav")); err == nil {
		t.Fatal("a rejected upload must not be written to disk")
	}
}

func TestHoldMusicUploadRejectsMissingField(t *testing.T) {
	s, _ := holdMusicServer(t)
	body, ctype := multipartBody(t, "wrongfield", "hold.wav", wavFixture(1, 16000, 1600))
	req := httptest.NewRequest("POST", "/api/holdmusic", body)
	req.Header.Set("Content-Type", ctype)
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, req)
	if rec.Code != 400 {
		t.Fatalf("missing file field: want 400, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestHoldMusicUploadRejectsOversizedBody(t *testing.T) {
	s, dir := holdMusicServer(t)
	// A valid WAV, just past the 8 MiB cap this route allows.
	frames := (maxHoldMusicBytes / 2) + 1024
	rec := postHoldMusic(t, s, wavFixture(1, 16000, frames))
	if rec.Code != 413 {
		t.Fatalf("oversized upload: want 413, got %d %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "hold-music.wav")); err == nil {
		t.Fatal("an oversized upload must not be written to disk")
	}
}

func TestHoldMusicGetReportsDefaultAndUploaded(t *testing.T) {
	s, _ := holdMusicServer(t)

	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, httptest.NewRequest("GET", "/api/holdmusic", nil))
	var before struct {
		Exists bool `json:"exists"`
		Size   int  `json:"size"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &before); err != nil {
		t.Fatal(err)
	}
	if before.Exists {
		t.Fatal("no upload yet, exists must be false")
	}

	if code := postHoldMusic(t, s, wavFixture(1, 16000, 1600)).Code; code != 200 {
		t.Fatalf("upload: want 200, got %d", code)
	}

	rec = httptest.NewRecorder()
	s.routes().ServeHTTP(rec, httptest.NewRequest("GET", "/api/holdmusic", nil))
	var after struct {
		Exists bool `json:"exists"`
		Size   int  `json:"size"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &after); err != nil {
		t.Fatal(err)
	}
	if !after.Exists || after.Size == 0 {
		t.Fatalf("after upload: %+v, want exists with a non-zero size", after)
	}
}
