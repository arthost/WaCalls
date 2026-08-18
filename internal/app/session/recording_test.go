package session

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestRecordingFileNameKeepsRealCallIDs(t *testing.T) {
	// WhatsApp call IDs are uppercase hex; our transfer/test IDs add dashes.
	for _, id := range []string{
		"3EB0C767D82B0F3A1234",
		"call-abc_123",
		"CALL1",
	} {
		if got, want := recordingFileName(id), id+".wav"; got != want {
			t.Errorf("recordingFileName(%q) = %q, want %q", id, got, want)
		}
	}
}

func TestRecordingFileNameStripsPathEscapes(t *testing.T) {
	// Inbound call IDs come from the peer's offer stanza, so they are attacker-chosen
	// text: the sanitised leaf must never escape the recordings directory or land on
	// a path the caller picked.
	cases := []string{
		"../../etc/passwd",
		`..\..\windows\system32\cfg`,
		"/absolute/path",
		"a/b/c",
		"call id with spaces",
		"call\x00id",
		"call.wav.wav",
		"C:evil",
	}
	dir := filepath.Join("data", "recordings")
	for _, id := range cases {
		name := recordingFileName(id)
		if name == "" {
			continue // fully rejected, which is also fine
		}
		if strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
			t.Errorf("recordingFileName(%q) = %q, which still carries path syntax", id, name)
			continue
		}
		full := filepath.Join(dir, name)
		if filepath.Dir(full) != dir {
			t.Errorf("recordingFileName(%q) = %q, which resolves outside %q", id, name, dir)
		}
		if !strings.HasSuffix(name, ".wav") {
			t.Errorf("recordingFileName(%q) = %q, want a .wav leaf", id, name)
		}
	}
}

func TestRecordingFileNameRejectsIDsWithNothingUsable(t *testing.T) {
	for _, id := range []string{"", "../", "///", "...", "!@#$%"} {
		if got := recordingFileName(id); got != "" {
			t.Errorf("recordingFileName(%q) = %q, want a rejection so no file is opened", id, got)
		}
	}
}
