package call

import (
	"sort"
	"sync"
	"time"
)

const (
	// rxSpeechRMS: limiar de fala (~-40 dBFS). Mesmo critério da telemetria de
	// referência para detectar silêncio/ruído de conforto vs fala real.
	rxSpeechRMS = 0.01
	// rxSpeechGap: intervalo entre dois pacotes de fala acima disso é buraco.
	rxSpeechGap = 600 * time.Millisecond
)

// RxStats é a telemetria do áudio recebido do peer numa chamada. Separa buraco
// vindo do lado WhatsApp (celular → relay → servidor) de eventuais problemas
// no trecho servidor → navegador.
type RxStats struct {
	Packets      int64         `json:"packets"`
	SpeechGaps   int64         `json:"speechGaps"`
	MaxSpeechGap time.Duration `json:"maxSpeechGap"`
	Undecodable  int64         `json:"undecodable"`
	Relocks      int64         `json:"relocks"`
	Relay        string        `json:"relay"`
}

// rxStats acumula RxStats no caminho quente. Mutex próprio para não concorrer
// com o mutex principal do CallManager. Sem alocação por pacote.
type rxStats struct {
	mu          sync.Mutex
	packets     int64
	gaps        int64
	maxGapNs    int64
	undecodable int64
	relocks     int64
	lastNs      int64
	lastSpeech  bool
	byRelay     map[string]int64
}

// decoded registra um pacote decodificado com sucesso. arrivalNs é o instante
// de chegada (antes do decode): relays diferentes decodificam em paralelo,
// então um pacote pode terminar fora de ordem — esse não move o relógio para trás.
func (s *rxStats) decoded(arrivalNs int64, pcm []float32, relay string) {
	speech := isSpeech(pcm)
	s.mu.Lock()
	s.packets++
	if s.byRelay == nil {
		s.byRelay = make(map[string]int64, 4)
	}
	s.byRelay[relay]++
	if arrivalNs > s.lastNs {
		// Intervalo que começa ou termina em silêncio é DTX, não buraco.
		if speech && s.lastSpeech {
			if gap := arrivalNs - s.lastNs; gap > int64(rxSpeechGap) {
				s.gaps++
				s.maxGapNs = max(s.maxGapNs, gap)
			}
		}
		s.lastNs = arrivalNs
		s.lastSpeech = speech
	}
	s.mu.Unlock()
}

func (s *rxStats) dropped() {
	s.mu.Lock()
	s.undecodable++
	s.mu.Unlock()
}

func (s *rxStats) relocked() {
	s.mu.Lock()
	s.relocks++
	s.mu.Unlock()
}

func (s *rxStats) snapshot() RxStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := RxStats{
		Packets:      s.packets,
		SpeechGaps:   s.gaps,
		MaxSpeechGap: time.Duration(s.maxGapNs),
		Undecodable:  s.undecodable,
		Relocks:      s.relocks,
	}
	names := make([]string, 0, len(s.byRelay))
	for name := range s.byRelay {
		names = append(names, name)
	}
	sort.Strings(names) // ordem determinística em caso de empate
	var best int64
	for _, name := range names {
		if n := s.byRelay[name]; n > best {
			best, out.Relay = n, name
		}
	}
	return out
}

func isSpeech(pcm []float32) bool {
	if len(pcm) == 0 {
		return false
	}
	var sum float64
	for _, v := range pcm {
		sum += float64(v) * float64(v)
	}
	return sum/float64(len(pcm)) > rxSpeechRMS*rxSpeechRMS
}
