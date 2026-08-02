package media

import (
	"encoding/binary"
	"io"
	"os"
	"sync"
	"time"
)

const (
	recSampleRate   = 16000
	recFrameSamples = recSampleRate / 50 // 20 ms grid = 320 samples
	recMaxBuffered  = recSampleRate * 2  // cap each leg at 2 s to bound memory
	wavHeaderLen    = 44
)

// WavRecorder mixes two mono 16 kHz float32 audio legs (the WhatsApp peer and
// the browser operator) into a single mono 16-bit PCM WAV file on disk. Each
// leg is fed independently as its audio arrives (peer via OnPeerAudio, operator
// via OnBrowserPCM); a background pump drains both buffers on a 20 ms grid, sums
// them sample-for-sample (clamped to int16 by floatToInt16), and appends the
// result. The WAV header is written up front with placeholder sizes and patched
// with the true data length on Close.
type WavRecorder struct {
	mu       sync.Mutex
	f        *os.File
	peerBuf  []float32
	opBuf    []float32
	dataLen  uint32
	stopped  bool
	writeErr error

	stop chan struct{}
	done chan struct{}
}

// NewWavRecorder creates the WAV file at path and starts the mixing pump. The
// caller must Close it to flush the tail and patch the header.
func NewWavRecorder(path string) (*WavRecorder, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	if err := writeWavHeader(f, 0); err != nil {
		_ = f.Close()
		return nil, err
	}
	r := &WavRecorder{
		f:    f,
		stop: make(chan struct{}),
		done: make(chan struct{}),
	}
	go r.pump()
	return r, nil
}

// WritePeer feeds decoded peer (WhatsApp) audio into the mix.
func (r *WavRecorder) WritePeer(pcm []float32) { r.feed(&r.peerBuf, pcm) }

// WriteOperator feeds operator (browser mic) audio into the mix.
func (r *WavRecorder) WriteOperator(pcm []float32) { r.feed(&r.opBuf, pcm) }

func (r *WavRecorder) feed(dst *[]float32, pcm []float32) {
	if len(pcm) == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped {
		return
	}
	*dst = append(*dst, pcm...)
	if len(*dst) > recMaxBuffered {
		*dst = (*dst)[len(*dst)-recMaxBuffered:]
	}
}

func (r *WavRecorder) pump() {
	defer close(r.done)
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-r.stop:
			r.drain()
			return
		case <-ticker.C:
			r.tick()
		}
	}
}

func (r *WavRecorder) tick() {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Nothing buffered on either leg: skip the frame rather than pad the timeline
	// with silence, so pre-connect gaps don't bloat the file.
	if len(r.peerBuf) == 0 && len(r.opBuf) == 0 {
		return
	}
	peer := takeSamples(&r.peerBuf, recFrameSamples)
	op := takeSamples(&r.opBuf, recFrameSamples)
	r.writeMixLocked(peer, op)
}

// drain flushes whatever remains in both buffers on stop, one frame at a time.
func (r *WavRecorder) drain() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for len(r.peerBuf) > 0 || len(r.opBuf) > 0 {
		peer := takeSamples(&r.peerBuf, recFrameSamples)
		op := takeSamples(&r.opBuf, recFrameSamples)
		r.writeMixLocked(peer, op)
	}
}

func (r *WavRecorder) writeMixLocked(peer, op []float32) {
	n := len(peer)
	if len(op) > n {
		n = len(op)
	}
	if n == 0 || r.writeErr != nil {
		return
	}
	buf := make([]byte, n*2)
	for i := 0; i < n; i++ {
		var s float32
		if i < len(peer) {
			s += peer[i]
		}
		if i < len(op) {
			s += op[i]
		}
		binary.LittleEndian.PutUint16(buf[i*2:], uint16(floatToInt16(s)))
	}
	if _, err := r.f.Write(buf); err != nil {
		r.writeErr = err
		return
	}
	r.dataLen += uint32(n * 2)
}

// Close stops the pump, flushes the tail, patches the WAV header with the true
// data length, and closes the file. Safe to call more than once.
func (r *WavRecorder) Close() error {
	r.mu.Lock()
	if r.stopped {
		r.mu.Unlock()
		return nil
	}
	r.stopped = true
	r.mu.Unlock()

	close(r.stop)
	<-r.done

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.writeErr != nil {
		_ = r.f.Close()
		return r.writeErr
	}
	if err := writeWavHeader(r.f, r.dataLen); err != nil {
		_ = r.f.Close()
		return err
	}
	return r.f.Close()
}

// takeSamples removes and returns up to n samples from the front of *buf.
func takeSamples(buf *[]float32, n int) []float32 {
	if len(*buf) == 0 {
		return nil
	}
	if len(*buf) < n {
		n = len(*buf)
	}
	out := make([]float32, n)
	copy(out, (*buf)[:n])
	*buf = (*buf)[n:]
	return out
}

func writeWavHeader(f *os.File, dataLen uint32) error {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	var h [wavHeaderLen]byte
	copy(h[0:], "RIFF")
	binary.LittleEndian.PutUint32(h[4:], 36+dataLen)
	copy(h[8:], "WAVE")
	copy(h[12:], "fmt ")
	binary.LittleEndian.PutUint32(h[16:], 16) // PCM fmt chunk size
	binary.LittleEndian.PutUint16(h[20:], 1)  // audio format = PCM
	binary.LittleEndian.PutUint16(h[22:], 1)  // channels = mono
	binary.LittleEndian.PutUint32(h[24:], recSampleRate)
	binary.LittleEndian.PutUint32(h[28:], recSampleRate*2) // byte rate = rate*blockAlign
	binary.LittleEndian.PutUint16(h[32:], 2)               // block align = channels*bytesPerSample
	binary.LittleEndian.PutUint16(h[34:], 16)              // bits per sample
	copy(h[36:], "data")
	binary.LittleEndian.PutUint32(h[40:], dataLen)
	if _, err := f.Write(h[:]); err != nil {
		return err
	}
	return nil
}
