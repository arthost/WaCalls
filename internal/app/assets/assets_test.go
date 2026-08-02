package assets

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureHoldMusicWAVFile(t *testing.T) {
	data := GenerateDefaultHoldMusicWAV()
	if len(data) == 0 {
		t.Fatal("generated WAV is empty")
	}
	err := os.WriteFile("hold-music.wav", data, 0644)
	if err != nil {
		t.Fatalf("failed to write hold-music.wav: %v", err)
	}
}

func TestLoadHoldMusic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hold-music.wav")
	_ = EnsureHoldMusicFile(dir)
	pcm := LoadHoldMusic(path)
	if len(pcm) == 0 {
		t.Error("LoadHoldMusic returned empty PCM")
	}
}
