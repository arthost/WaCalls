package app

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"wacalls/internal/app/events"
	"wacalls/internal/app/session"
	"wacalls/internal/voip/media"
)

func recordingsServer(t *testing.T) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	return &Server{
		authorize: bearerAuthorizer(""),
		broker:    events.NewBroker(nil, slog.Default()),
		sessions:  session.NewManager(session.Deps{RecordingsDir: dir}),
		dataDir:   t.TempDir(),
	}, dir
}

func getRecordings(t *testing.T, s *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
	return rec
}

func TestRecordingsListReportsFilesOnDisk(t *testing.T) {
	s, dir := recordingsServer(t)

	rec := getRecordings(t, s, "/api/recordings")
	if rec.Code != 200 {
		t.Fatalf("empty listing: want 200, got %d %s", rec.Code, rec.Body.String())
	}
	// An empty array, not null: the client iterates the field without a nil check.
	if body := rec.Body.String(); !strings.Contains(body, `"recordings":[]`) {
		t.Fatalf("empty listing = %s, want an empty JSON array", strings.TrimSpace(body))
	}

	wav := media.EncodeWav16kMono(make([]float32, 1600))
	if err := os.WriteFile(filepath.Join(dir, "CALL1.wav"), wav, 0644); err != nil {
		t.Fatal(err)
	}

	rec = getRecordings(t, s, "/api/recordings")
	var resp struct {
		Recordings []struct {
			CallID     string `json:"callId"`
			Size       int64  `json:"size"`
			ModifiedAt int64  `json:"modifiedAt"`
			Active     bool   `json:"active"`
		} `json:"recordings"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Recordings) != 1 {
		t.Fatalf("listing has %d entries, want 1: %s", len(resp.Recordings), rec.Body.String())
	}
	got := resp.Recordings[0]
	if got.CallID != "CALL1" || got.Size != int64(len(wav)) || got.ModifiedAt == 0 || got.Active {
		t.Fatalf("entry = %+v, want CALL1 sized %d, a mtime, and not active", got, len(wav))
	}
}

func TestRecordingDownloadServesWavBytes(t *testing.T) {
	s, dir := recordingsServer(t)
	wav := media.EncodeWav16kMono(make([]float32, 800))
	if err := os.WriteFile(filepath.Join(dir, "CALL2.wav"), wav, 0644); err != nil {
		t.Fatal(err)
	}

	rec := getRecordings(t, s, "/api/recordings/CALL2")
	if rec.Code != 200 {
		t.Fatalf("download: want 200, got %d %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "audio/wav" {
		t.Errorf("content-type = %q, want audio/wav — the runtime image has no mime entry for .wav", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, `filename="CALL2.wav"`) {
		t.Errorf("content-disposition = %q, want a CALL2.wav file name", cd)
	}
	if ar := rec.Header().Get("Accept-Ranges"); ar != "bytes" {
		t.Errorf("accept-ranges = %q, want bytes so a player can seek in a long call", ar)
	}
	if rec.Header().Get("X-Recording-Active") != "" {
		t.Error("a finished recording must not be flagged active")
	}
	if !bytes.Equal(rec.Body.Bytes(), wav) {
		t.Error("served bytes differ from the file on disk")
	}
}

func TestRecordingDownloadRejectsUnknownAndTraversal(t *testing.T) {
	s, dir := recordingsServer(t)
	// The escape target exists and holds something worth stealing, so a handler that
	// trusted the raw path value would serve it.
	outside := filepath.Join(filepath.Dir(dir), "escape.wav")
	if err := os.WriteFile(outside, []byte("secret"), 0644); err != nil {
		t.Fatal(err)
	}

	// %2F keeps the traversal inside a single path segment, so it survives the mux's
	// path cleaning and reaches the handler as "../escape".
	for _, path := range []string{
		"/api/recordings/nope",
		"/api/recordings/..%2Fescape",
		"/api/recordings/..%2F..%2Fetc%2Fpasswd",
		"/api/recordings/%2e%2e%2Fescape",
		"/api/recordings/../escape",
	} {
		rec := getRecordings(t, s, path)
		if rec.Code == 200 {
			t.Errorf("GET %s returned 200, want a rejection", path)
		}
		if strings.Contains(rec.Body.String(), "secret") {
			t.Errorf("GET %s served a file from outside the recordings dir", path)
		}
	}
}
