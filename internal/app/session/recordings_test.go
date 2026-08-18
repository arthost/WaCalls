package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"wacalls/internal/voip/media"
)

func recordingsManager(t *testing.T) (*Manager, string) {
	t.Helper()
	dir := t.TempDir()
	return NewManager(Deps{RecordingsDir: dir}), dir
}

// writeRecording drops a file in the recordings dir with a controlled mtime, so the
// ordering assertions below do not depend on how fast the test runs.
func writeRecording(t *testing.T, dir, name string, size int, age time.Duration) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, make([]byte, size), 0644); err != nil {
		t.Fatal(err)
	}
	when := time.Now().Add(-age)
	if err := os.Chtimes(p, when, when); err != nil {
		t.Fatal(err)
	}
}

func TestListRecordingsIsEmptyBeforeAnythingIsRecorded(t *testing.T) {
	// RecordingsDir is only created when the first call starts recording, so listing
	// must treat "no dir yet" as an empty result instead of a server error.
	m := NewManager(Deps{RecordingsDir: filepath.Join(t.TempDir(), "recordings")})
	recs, err := m.ListRecordings()
	if err != nil {
		t.Fatalf("missing recordings dir must not be an error: %v", err)
	}
	if len(recs) != 0 {
		t.Fatalf("got %d recordings from a dir that does not exist", len(recs))
	}
}

func TestListRecordingsNewestFirstAndOnlyWavs(t *testing.T) {
	m, dir := recordingsManager(t)
	writeRecording(t, dir, "OLD1.wav", 100, 2*time.Hour)
	writeRecording(t, dir, "NEW1.wav", 200, time.Minute)
	writeRecording(t, dir, "MID1.wav", 300, 30*time.Minute)
	// Neither of these is a recording and neither may show up in the listing.
	writeRecording(t, dir, "notes.txt", 10, time.Minute)
	if err := os.Mkdir(filepath.Join(dir, "stale.wav"), 0755); err != nil {
		t.Fatal(err)
	}

	recs, err := m.ListRecordings()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range recs {
		got = append(got, r.CallID)
	}
	want := []string{"NEW1", "MID1", "OLD1"}
	if len(got) != len(want) {
		t.Fatalf("listing = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("listing = %v, want newest first %v", got, want)
		}
	}
	if recs[0].Size != 200 {
		t.Errorf("NEW1 size = %d, want 200", recs[0].Size)
	}
	if recs[0].ModifiedAt == 0 {
		t.Error("modifiedAt must be set so the UI can show when the call happened")
	}
	if recs[0].Active {
		t.Error("no session is recording, so nothing may be flagged active")
	}
}

func TestListRecordingsFlagsInProgressRecording(t *testing.T) {
	m, dir := recordingsManager(t)
	// A finished recording, plus two a session still holds open.
	writeRecording(t, dir, "DONE1.wav", 44, time.Minute)
	live, err := media.NewWavRecorder(filepath.Join(dir, "LIVE1.wav"))
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	// A call ID carrying characters the sanitiser strips still has to line up with the
	// file it actually writes, or the flag silently reads false mid-call.
	dirty, err := media.NewWavRecorder(filepath.Join(dir, "callX1.wav"))
	if err != nil {
		t.Fatal(err)
	}
	defer dirty.Close()
	m.sessions["s1"] = &Session{id: "s1", mgr: m, recorders: map[string]*media.WavRecorder{
		"LIVE1":    live,
		"call/X1@": dirty,
	}}

	recs, err := m.ListRecordings()
	if err != nil {
		t.Fatal(err)
	}
	active := map[string]bool{}
	for _, r := range recs {
		active[r.CallID] = r.Active
	}
	if len(recs) != 3 {
		t.Fatalf("listing has %d entries, want 3: %+v", len(recs), recs)
	}
	if !active["LIVE1"] || !active["callX1"] {
		t.Errorf("in-progress recordings not flagged: %v", active)
	}
	if active["DONE1"] {
		t.Error("a closed recording must not be flagged active")
	}

	// RecordingPath has to agree, so a mid-call download can warn the same way.
	if _, isActive, err := m.RecordingPath("LIVE1"); err != nil || !isActive {
		t.Errorf("RecordingPath(LIVE1) = active %v, err %v; want active with no error", isActive, err)
	}
	if _, isActive, err := m.RecordingPath("DONE1"); err != nil || isActive {
		t.Errorf("RecordingPath(DONE1) = active %v, err %v; want inactive with no error", isActive, err)
	}
}

func TestRecordingPathStaysInsideRecordingsDir(t *testing.T) {
	m, dir := recordingsManager(t)
	writeRecording(t, dir, "CALL1.wav", 44, time.Minute)

	path, active, err := m.RecordingPath("CALL1")
	if err != nil {
		t.Fatalf("known recording: %v", err)
	}
	if active {
		t.Error("nothing is recording, active must be false")
	}
	if want := filepath.Join(dir, "CALL1.wav"); path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}

	// The escape target exists, so a resolver that trusted the raw ID would hand it out.
	outside := filepath.Join(filepath.Dir(dir), "escape.wav")
	if err := os.WriteFile(outside, []byte("secret"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"../escape", `..\escape`, "../../etc/passwd", "/etc/passwd", "unknown", "", "///", ".."} {
		if p, _, err := m.RecordingPath(id); err == nil {
			t.Errorf("RecordingPath(%q) resolved to %q, want a rejection", id, p)
		}
	}
}
