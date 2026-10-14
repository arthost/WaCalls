package app

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"wacalls/internal/voip/call"

	"github.com/coder/websocket"
)

const (
	geminiLiveEndpoint = "wss://generativelanguage.googleapis.com/ws/google.ai.generativelanguage.v1alpha.GenerativeService.BidiGenerateContent"
	defaultGeminiModel = "gemini-2.0-flash-exp"
	defaultGeminiVoice = "Puck"
)

// GeminiLiveSession manages real-time bidirectional audio streaming between WhatsApp and Google Gemini.
type GeminiLiveSession struct {
	apiKey         string
	model          string
	voice          string
	prompt         string
	initialMessage string
	cm             *call.CallManager
	log            *slog.Logger

	ctx        context.Context
	cancel     context.CancelFunc
	wsConn     *websocket.Conn
	mu         sync.Mutex
	closed     bool
	interacted bool

	audioBuffer []byte
	bufMu       sync.Mutex
}

func NewGeminiLiveSession(
	apiKey, model, voice, prompt, initialMessage string,
	cm *call.CallManager,
	log *slog.Logger,
) *GeminiLiveSession {
	if model == "" {
		model = defaultGeminiModel
	}
	if voice == "" {
		voice = defaultGeminiVoice
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &GeminiLiveSession{
		apiKey:         apiKey,
		model:          model,
		voice:          voice,
		prompt:         prompt,
		initialMessage: initialMessage,
		cm:             cm,
		log:            log,
		ctx:            ctx,
		cancel:         cancel,
	}
}

// Start opens the WebSocket connection to Gemini Live, performs setup, and begins streaming.
func (g *GeminiLiveSession) Start() error {
	wsURL := fmt.Sprintf("%s?key=%s", geminiLiveEndpoint, g.apiKey)

	ctxConnect, cancelConnect := context.WithTimeout(g.ctx, 15*time.Second)
	defer cancelConnect()

	conn, resp, err := websocket.Dial(ctxConnect, wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"User-Agent": []string{"DuoCRM-Calls/1.0"}},
	})
	if err != nil {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		g.log.Error("gemini live dial failed", "status", status, "err", err)
		return fmt.Errorf("gemini dial failed (status %d): %w", status, err)
	}
	g.wsConn = conn

	// Send setup handshake
	if err := g.sendSetup(); err != nil {
		_ = conn.Close(websocket.StatusInternalError, "setup failed")
		return fmt.Errorf("gemini setup error: %w", err)
	}

	// Read setupComplete
	if err := g.waitSetupComplete(); err != nil {
		_ = conn.Close(websocket.StatusInternalError, "setup incomplete")
		return fmt.Errorf("gemini setup handshake incomplete: %w", err)
	}

	g.log.Info("gemini live session established", "model", g.model, "voice", g.voice)

	// If an initial greeting message is configured, ask Gemini to start speaking
	if g.initialMessage != "" {
		go g.sendGreeting()
	}

	// Start receive loop from Gemini
	go g.readGeminiLoop()

	return nil
}

// sendSetup transmits model, voice, and system instructions to Gemini.
func (g *GeminiLiveSession) sendSetup() error {
	modelPath := g.model
	if len(modelPath) < 7 || modelPath[:7] != "models/" {
		modelPath = "models/" + modelPath
	}

	sysPrompt := g.prompt
	if sysPrompt == "" {
		sysPrompt = "Você é um assistente virtual profissional do DuoCRM. Responda em português com fala natural, amigável, clara e concisa."
	}

	setupMsg := map[string]any{
		"setup": map[string]any{
			"model": modelPath,
			"generationConfig": map[string]any{
				"responseModalities": []string{"AUDIO"},
				"speechConfig": map[string]any{
					"voiceConfig": map[string]any{
						"prebuiltVoiceConfig": map[string]any{
							"voiceName": g.voice,
						},
					},
				},
			},
			"systemInstruction": map[string]any{
				"parts": []map[string]any{
					{"text": sysPrompt},
				},
			},
		},
	}

	data, err := json.Marshal(setupMsg)
	if err != nil {
		return err
	}

	ctxSend, cancel := context.WithTimeout(g.ctx, 5*time.Second)
	defer cancel()
	return g.wsConn.Write(ctxSend, websocket.MessageText, data)
}

func (g *GeminiLiveSession) waitSetupComplete() error {
	ctxWait, cancel := context.WithTimeout(g.ctx, 10*time.Second)
	defer cancel()

	_, data, err := g.wsConn.Read(ctxWait)
	if err != nil {
		return err
	}

	var res map[string]any
	if err := json.Unmarshal(data, &res); err != nil {
		return err
	}
	if _, ok := res["setupComplete"]; !ok {
		g.log.Warn("gemini unexpected first response", "body", string(data))
	}
	return nil
}

func (g *GeminiLiveSession) sendGreeting() {
	time.Sleep(300 * time.Millisecond)
	promptText := fmt.Sprintf("Inicie a conversa agora dizendo amigavelmente: %s", g.initialMessage)

	turnMsg := map[string]any{
		"clientContent": map[string]any{
			"turns": []map[string]any{
				{
					"role": "user",
					"parts": []map[string]any{
						{"text": promptText},
					},
				},
			},
			"turnComplete": true,
		},
	}

	data, err := json.Marshal(turnMsg)
	if err != nil {
		return
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed || g.wsConn == nil {
		return
	}
	ctxSend, cancel := context.WithTimeout(g.ctx, 5*time.Second)
	defer cancel()
	_ = g.wsConn.Write(ctxSend, websocket.MessageText, data)
}

// PushWhatsAppAudio buffers and streams 16kHz PCM audio from WhatsApp to Gemini Live.
func (g *GeminiLiveSession) PushWhatsAppAudio(pcm []float32) {
	g.mu.Lock()
	if g.closed || g.wsConn == nil {
		g.mu.Unlock()
		return
	}
	g.mu.Unlock()

	// Convert float32 [-1, 1] to Int16 LE bytes
	rawBytes := make([]byte, len(pcm)*2)
	for i, v := range pcm {
		if v > 1.0 {
			v = 1.0
		} else if v < -1.0 {
			v = -1.0
		}
		var iv int16
		if v < 0 {
			iv = int16(v * 32768)
		} else {
			iv = int16(v * 32767)
		}
		binary.LittleEndian.PutUint16(rawBytes[i*2:], uint16(iv))
	}

	g.bufMu.Lock()
	g.audioBuffer = append(g.audioBuffer, rawBytes...)
	// Send chunks of ~60ms (1920 bytes = 960 samples @ 16kHz mono)
	if len(g.audioBuffer) < 1920 {
		g.bufMu.Unlock()
		return
	}
	toSend := g.audioBuffer
	g.audioBuffer = nil
	g.bufMu.Unlock()

	b64Audio := base64.StdEncoding.EncodeToString(toSend)
	chunkMsg := map[string]any{
		"realtimeInput": map[string]any{
			"mediaChunks": []map[string]any{
				{
					"mimeType": "audio/pcm;rate=16000",
					"data":     b64Audio,
				},
			},
		},
	}

	data, err := json.Marshal(chunkMsg)
	if err != nil {
		return
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed || g.wsConn == nil {
		return
	}
	ctxWrite, cancel := context.WithTimeout(g.ctx, 2*time.Second)
	defer cancel()
	_ = g.wsConn.Write(ctxWrite, websocket.MessageText, data)
}

// readGeminiLoop listens for audio packets from Gemini and injects them into WhatsApp.
func (g *GeminiLiveSession) readGeminiLoop() {
	defer g.Close()

	for {
		select {
		case <-g.ctx.Done():
			return
		default:
		}

		_, data, err := g.wsConn.Read(g.ctx)
		if err != nil {
			g.log.Debug("gemini live ws closed or read error", "err", err)
			break
		}

		var payload struct {
			ServerContent struct {
				ModelTurn struct {
					Parts []struct {
						InlineData struct {
							MimeType string `json:"mimeType"`
							Data     string `json:"data"`
						} `json:"inlineData"`
						Text string `json:"text,omitempty"`
					} `json:"parts"`
				} `json:"modelTurn"`
				Interrupted  bool `json:"interrupted"`
				TurnComplete bool `json:"turnComplete"`
			} `json:"serverContent"`
		}

		if err := json.Unmarshal(data, &payload); err != nil {
			continue
		}

		// Handle user interruption (barge-in): Gemini detected user speaking
		if payload.ServerContent.Interrupted {
			g.log.Debug("gemini live: speech interrupted by user")
			continue
		}

		for _, part := range payload.ServerContent.ModelTurn.Parts {
			if part.InlineData.Data != "" {
				pcm24Bytes, err := base64.StdEncoding.DecodeString(part.InlineData.Data)
				if err != nil || len(pcm24Bytes) < 2 {
					continue
				}

				// Gemini audio is 24kHz Int16 LE mono. Resample to 16kHz float32 mono for WhatsApp.
				pcm16Float := resample24kTo16k(pcm24Bytes)
				if len(pcm16Float) > 0 && g.cm != nil {
					g.cm.FeedCapturedPCM(pcm16Float)
				}
			}
		}
	}
}

// resample24kTo16k converts 24kHz Int16 LE PCM bytes to 16kHz float32 samples.
func resample24kTo16k(b []byte) []float32 {
	numSamples24k := len(b) / 2
	if numSamples24k == 0 {
		return nil
	}

	samples24k := make([]float32, numSamples24k)
	for i := 0; i < numSamples24k; i++ {
		iv := int16(binary.LittleEndian.Uint16(b[i*2:]))
		samples24k[i] = float32(iv) / 32768.0
	}

	// Ratio 24000 / 16000 = 1.5. Output length = numSamples24k * 2 / 3
	outLen := (numSamples24k * 2) / 3
	if outLen == 0 {
		return nil
	}
	out := make([]float32, outLen)

	for i := 0; i < outLen; i++ {
		srcPos := float64(i) * 1.5
		idx0 := int(srcPos)
		idx1 := idx0 + 1
		if idx1 >= numSamples24k {
			idx1 = idx0
		}
		frac := float32(srcPos - float64(idx0))
		out[i] = (1.0-frac)*samples24k[idx0] + frac*samples24k[idx1]
	}

	return out
}

func (g *GeminiLiveSession) Close() {
	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		return
	}
	g.closed = true
	g.cancel()
	if g.wsConn != nil {
		_ = g.wsConn.Close(websocket.StatusNormalClosure, "call ended")
	}
	g.mu.Unlock()
}
