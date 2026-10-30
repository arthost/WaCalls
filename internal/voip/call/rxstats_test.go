package call

import (
	"testing"
	"time"
)

func TestRxStatsDecodedAndSpeechGaps(t *testing.T) {
	var s rxStats
	speechPCM := make([]float32, 160)
	for i := range speechPCM {
		speechPCM[i] = 0.1 // well above rxSpeechRMS (0.01)
	}
	silencePCM := make([]float32, 160)

	now := time.Now().UnixNano()
	s.decoded(now, speechPCM, "relay-a")
	s.decoded(now+int64(20*time.Millisecond), speechPCM, "relay-a")

	// Packet after a big gap (700ms > 600ms gap)
	s.decoded(now+int64(720*time.Millisecond), speechPCM, "relay-b")

	snap := s.snapshot()
	if snap.Packets != 3 {
		t.Fatalf("expected 3 packets, got %d", snap.Packets)
	}
	if snap.SpeechGaps != 1 {
		t.Fatalf("expected 1 speech gap, got %d", snap.SpeechGaps)
	}
	if snap.Relay != "relay-a" {
		t.Fatalf("expected relay-a as best relay, got %s", snap.Relay)
	}

	// Dropped packet
	s.dropped()
	s.relocked()
	snap2 := s.snapshot()
	if snap2.Undecodable != 1 {
		t.Fatalf("expected 1 undecodable, got %d", snap2.Undecodable)
	}
	if snap2.Relocks != 1 {
		t.Fatalf("expected 1 relock, got %d", snap2.Relocks)
	}

	// Silence should not produce speech gaps
	s.decoded(now+int64(2000*time.Millisecond), silencePCM, "relay-b")
	snap3 := s.snapshot()
	if snap3.SpeechGaps != 1 {
		t.Fatalf("silence should not increment speech gaps, got %d", snap3.SpeechGaps)
	}
}
