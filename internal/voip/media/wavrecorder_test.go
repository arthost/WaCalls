package media

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWavRecorder(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test-rec.wav")

	rec, err := NewWavRecorder(path)
	if err != nil {
		t.Fatalf("failed to create recorder: %v", err)
	}

	peerPCM := []float32{0.1, 0.2, 0.3, 0.4}
	opPCM := []float32{0.05, 0.05, 0.05, 0.05}

	rec.WritePeer(peerPCM)
	rec.WriteOperator(opPCM)

	time.Sleep(50 * time.Millisecond)

	if err := rec.Close(); err != nil {
		t.Fatalf("failed to close recorder: %v", err)
	}

	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("failed to stat recorder file: %v", err)
	}
	if st.Size() <= 44 {
		t.Errorf("expected recorder file size > 44, got %d", st.Size())
	}

	readPCM, err := ReadWavFloat32(path)
	if err != nil {
		t.Fatalf("failed to read recorded WAV: %v", err)
	}
	if len(readPCM) == 0 {
		t.Error("recorded PCM is empty")
	}
}
