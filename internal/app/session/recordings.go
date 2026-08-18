package session

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// RecordingInfo describes one WAV in RecordingsDir.
type RecordingInfo struct {
	CallID     string `json:"callId"`
	Size       int64  `json:"size"`
	ModifiedAt int64  `json:"modifiedAt"`
	Active     bool   `json:"active"`
}

// ListRecordings returns every recording on disk, newest first. Recordings still
// being written are flagged Active: their WAV header still carries the placeholder
// zero length that NewWavRecorder wrote, so most players treat the file as empty
// until Close patches it. Callers should surface that rather than hand out a file
// that looks broken.
func (m *Manager) ListRecordings() ([]RecordingInfo, error) {
	entries, err := os.ReadDir(m.RecordingsDir)
	if err != nil {
		if os.IsNotExist(err) {
			// Nothing recorded yet — an empty list, not an error.
			return []RecordingInfo{}, nil
		}
		return nil, err
	}
	active := m.activeRecordingIDs()
	out := make([]RecordingInfo, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".wav") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		callID := strings.TrimSuffix(e.Name(), ".wav")
		out = append(out, RecordingInfo{
			CallID:     callID,
			Size:       info.Size(),
			ModifiedAt: info.ModTime().UnixMilli(),
			Active:     active[callID],
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ModifiedAt > out[j].ModifiedAt })
	return out, nil
}

// activeRecordingIDs collects the call IDs every session is currently recording.
func (m *Manager) activeRecordingIDs() map[string]bool {
	m.mu.RLock()
	sessions := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		sessions = append(sessions, s)
	}
	m.mu.RUnlock()
	active := map[string]bool{}
	for _, s := range sessions {
		s.recMu.Lock()
		for callID := range s.recorders {
			// The file on disk is named after the sanitised ID, so key the map the
			// same way or the lookup silently misses for any ID we had to clean up.
			if name := recordingFileName(callID); name != "" {
				active[strings.TrimSuffix(name, ".wav")] = true
			}
		}
		s.recMu.Unlock()
	}
	return active
}

// RecordingPath resolves a call ID to its WAV on disk. It applies the same
// sanitisation as StartRecording, so a caller-supplied ID cannot walk out of
// RecordingsDir, and reports whether the recording is still being written.
func (m *Manager) RecordingPath(callID string) (path string, active bool, err error) {
	name := recordingFileName(callID)
	if name == "" {
		return "", false, fmt.Errorf("call id %q is not usable as a file name", callID)
	}
	path = filepath.Join(m.RecordingsDir, name)
	st, err := os.Stat(path)
	if err != nil {
		return "", false, err
	}
	if st.IsDir() {
		return "", false, fmt.Errorf("%q is not a recording", name)
	}
	return path, m.activeRecordingIDs()[strings.TrimSuffix(name, ".wav")], nil
}
