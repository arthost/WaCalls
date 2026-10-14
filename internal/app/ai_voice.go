package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"wacalls/internal/app/events"
	"wacalls/internal/app/session"
)

// AIVoiceConfig stores session-level voice AI preferences.
type AIVoiceConfig struct {
	Enabled        bool   `json:"enabled"`
	APIKey         string `json:"api_key"`
	Model          string `json:"model"`
	Voice          string `json:"voice"`
	Prompt         string `json:"prompt"`
	InitialMessage string `json:"initial_message"`
	AutoAnswer     bool   `json:"auto_answer"`
	Record         bool   `json:"record"`
	MaxDurationSec int    `json:"max_duration_sec"`
}

// Global registry of active Gemini Live sessions by call ID
var (
	activeGeminiSessions   = make(map[string]*GeminiLiveSession)
	activeGeminiSessionsMu sync.Mutex
)

func registerGeminiSession(callID string, g *GeminiLiveSession) {
	activeGeminiSessionsMu.Lock()
	defer activeGeminiSessionsMu.Unlock()
	activeGeminiSessions[callID] = g
}

func getGeminiSession(callID string) *GeminiLiveSession {
	activeGeminiSessionsMu.Lock()
	defer activeGeminiSessionsMu.Unlock()
	return activeGeminiSessions[callID]
}

func removeGeminiSession(callID string) *GeminiLiveSession {
	activeGeminiSessionsMu.Lock()
	defer activeGeminiSessionsMu.Unlock()
	g := activeGeminiSessions[callID]
	delete(activeGeminiSessions, callID)
	return g
}

// aiConfigPath returns the JSON persistence path for a given session.
func (s *Server) aiConfigPath(sid string) string {
	return filepath.Join(s.dataDir, fmt.Sprintf("ai-config-%s.json", sid))
}

func (s *Server) getAIConfig(sid string) AIVoiceConfig {
	cfg := AIVoiceConfig{
		Enabled:        false,
		Model:          "gemini-2.0-flash",
		Voice:          "Puck",
		Prompt:         "Você é um assistente virtual do DuoCRM. Atenda o cliente de forma amigável, clara e concisa em português.",
		AutoAnswer:     false,
		Record:         true,
		MaxDurationSec: 300,
	}

	data, err := os.ReadFile(s.aiConfigPath(sid))
	if err == nil {
		_ = json.Unmarshal(data, &cfg)
	}
	return cfg
}

func (s *Server) saveAIConfig(sid string, cfg AIVoiceConfig) error {
	_ = os.MkdirAll(s.dataDir, 0755)
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.aiConfigPath(sid), data, 0644)
}

// handleGetAIConfig returns current AI voice settings for the session.
func (s *Server) handleGetAIConfig(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("sid")
	cfg := s.getAIConfig(sid)
	writeJSON(w, http.StatusOK, cfg)
}

// handleSaveAIConfig saves AI voice settings for the session.
func (s *Server) handleSaveAIConfig(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("sid")
	var cfg AIVoiceConfig
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid payload"})
		return
	}
	if err := s.saveAIConfig(sid, cfg); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "config": cfg})
}

// handleStartAICall initiates an outbound call with Gemini Live attached once connected.
func (s *Server) handleStartAICall(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionByID(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	if !sess.IsPaired() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "session not paired"})
		return
	}

	var body struct {
		Phone          string `json:"phone"`
		Prompt         string `json:"prompt,omitempty"`
		InitialMessage string `json:"initial_message,omitempty"`
		Voice          string `json:"voice,omitempty"`
		Model          string `json:"model,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Phone) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "phone required"})
		return
	}

	phone := normalizePhone(body.Phone)
	cfg := s.getAIConfig(sess.ID())
	if !cfg.Enabled && body.Prompt == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "AI voice is not enabled for this session"})
		return
	}

	st, err := sess.StartCall(r.Context(), phone, false)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	prompt := cfg.Prompt
	if body.Prompt != "" {
		prompt = body.Prompt
	}
	initMsg := cfg.InitialMessage
	if body.InitialMessage != "" {
		initMsg = body.InitialMessage
	}
	voice := cfg.Voice
	if body.Voice != "" {
		voice = body.Voice
	}
	model := cfg.Model
	if body.Model != "" {
		model = body.Model
	}

	// Wait for call to connect in background and start Gemini Live
	go s.attachGeminiOnConnect(sess, st.CallID, phone, cfg.APIKey, model, voice, prompt, initMsg, cfg.Record)

	writeJSON(w, http.StatusOK, map[string]any{
		"callId": st.CallID,
		"phone":  phone,
		"status": "ringing",
	})
}

func (s *Server) attachGeminiOnConnect(
	sess *session.Session,
	callID, phone, apiKey, model, voice, prompt, initMsg string,
	record bool,
) {
	// Wait up to 45 seconds for peer to answer (status connected)
	deadline := time.Now().Add(45 * time.Second)
	answered := false

	for time.Now().Before(deadline) {
		rec, ok := s.broker.GetCall(callID)
		if !ok || rec.Status == events.StatusEnded {
			break
		}
		if rec.Status == events.StatusConnected {
			answered = true
			break
		}
		time.Sleep(150 * time.Millisecond)
	}

	if !answered {
		s.log.Info("ai call not answered, skipping gemini live", "call", callID, "phone", phone)
		return
	}

	s.log.Info("ai call answered, launching gemini live session", "call", callID, "phone", phone)

	cm, ok := sess.GetCallManager(callID)
	if !ok {
		return
	}

	if record {
		_ = sess.StartRecording(callID)
	}

	gemini := NewGeminiLiveSession(apiKey, model, voice, prompt, initMsg, cm, s.log)
	registerGeminiSession(callID, gemini)

	if err := gemini.Start(); err != nil {
		s.log.Error("failed to start gemini live session", "call", callID, "err", err)
		removeGeminiSession(callID)
		return
	}

	// Wire audio from WhatsApp into Gemini
	origOnPeerAudio := cm.OnPeerAudio
	cm.OnPeerAudio = func(pcm16 []float32) {
		if origOnPeerAudio != nil {
			origOnPeerAudio(pcm16)
		}
		if g := getGeminiSession(callID); g != nil {
			g.PushWhatsAppAudio(pcm16)
		}
	}
}

// AIBroadcastRequest defines bulk voice call parameters.
type AIBroadcastRequest struct {
	Numbers        []string `json:"numbers"`
	Concurrency    int      `json:"concurrency"`
	IntervalSec    int      `json:"interval_sec"`
	Prompt         string   `json:"prompt"`
	InitialMessage string   `json:"initial_message"`
	Voice          string   `json:"voice"`
	Model          string   `json:"model"`
	Record         bool     `json:"record"`
}

// handleAIBroadcast processes mass AI voice campaigns with anti-ban pacing.
func (s *Server) handleAIBroadcast(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionByID(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	if !sess.IsPaired() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "session not paired"})
		return
	}

	var req AIBroadcastRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Numbers) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "numbers required"})
		return
	}

	cfg := s.getAIConfig(sess.ID())
	if cfg.APIKey == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "gemini api key not configured"})
		return
	}

	campaignID := fmt.Sprintf("ai-camp-%d", time.Now().Unix())
	go s.runAIBroadcastCampaign(sess, campaignID, req, cfg)

	writeJSON(w, http.StatusOK, map[string]any{
		"campaignId": campaignID,
		"total":      len(req.Numbers),
		"status":     "running",
	})
}

func (s *Server) runAIBroadcastCampaign(
	sess *session.Session,
	campaignID string,
	req AIBroadcastRequest,
	cfg AIVoiceConfig,
) {
	conc := req.Concurrency
	if conc < 1 {
		conc = 1
	}
	if conc > 5 {
		conc = 5
	}
	interval := req.IntervalSec
	if interval <= 0 {
		interval = 15
	}

	sem := make(chan struct{}, conc)
	var wg sync.WaitGroup

	for _, rawNum := range req.Numbers {
		phone := normalizePhone(rawNum)
		if phone == "" {
			continue
		}

		sem <- struct{}{}
		wg.Add(1)

		go func(target string) {
			defer wg.Done()
			defer func() { <-sem }()

			s.dialOneAICall(sess, target, req, cfg)
		}(phone)

		// Anti-ban pacing: base interval + random jitter (2 to 8 seconds)
		jitter := 2 + rand.Intn(7)
		sleepDur := time.Duration(interval+jitter) * time.Second
		time.Sleep(sleepDur)
	}

	wg.Wait()
	s.log.Info("ai broadcast campaign finished", "campaignId", campaignID, "total", len(req.Numbers))
}

func (s *Server) dialOneAICall(
	sess *session.Session,
	phone string,
	req AIBroadcastRequest,
	cfg AIVoiceConfig,
) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	st, err := sess.StartCall(ctx, phone, false)
	if err != nil {
		s.log.Warn("ai campaign dial failed", "phone", phone, "err", err)
		return
	}

	prompt := cfg.Prompt
	if req.Prompt != "" {
		prompt = req.Prompt
	}
	initMsg := cfg.InitialMessage
	if req.InitialMessage != "" {
		initMsg = req.InitialMessage
	}
	voice := cfg.Voice
	if req.Voice != "" {
		voice = req.Voice
	}
	model := cfg.Model
	if req.Model != "" {
		model = req.Model
	}

	s.attachGeminiOnConnect(sess, st.CallID, phone, cfg.APIKey, model, voice, prompt, initMsg, req.Record || cfg.Record)
}

// NotifyCRMAICallEnded posts call completion details to DuoCRM Rails.
func (s *Server) NotifyCRMAICallEnded(phone, callID string, durationSec int, recordingURL, model string) {
	crmURL := os.Getenv("CRM_INTERNAL_URL")
	if crmURL == "" {
		crmURL = "http://crm:3000/api/v1/internal/calls/webhook"
	}

	payload := map[string]any{
		"event": "call.ended",
		"call": map[string]any{
			"phone":        phone,
			"call_id":      callID,
			"duration":     durationSec,
			"call_type":    "ai_voice",
			"ai_model":     model,
			"recordingUrl": recordingURL,
		},
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return
	}

	go func() {
		client := &http.Client{Timeout: 5 * time.Second}
		req, err := http.NewRequest("POST", crmURL, bytes.NewReader(data))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
		}
	}()
}

// handleIncomingCallAI is called whenever a new incoming call is offered to a session.
func (s *Server) handleIncomingCallAI(sessionID, callID, peer string) {
	cfg := s.getAIConfig(sessionID)
	if !cfg.Enabled || !cfg.AutoAnswer || cfg.APIKey == "" {
		return
	}

	s.log.Info("incoming call auto-answer triggered by AI", "session", sessionID, "call", callID, "peer", peer)

	// Small pause so WhatsApp media setup stabilizes
	time.Sleep(800 * time.Millisecond)

	sess, ok := s.sessions.Get(sessionID)
	if !ok || sess == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := sess.AcceptCall(ctx, callID); err != nil {
		s.log.Error("failed to auto-answer call for AI", "call", callID, "err", err)
		return
	}

	// Attach Gemini once connected
	s.attachGeminiOnConnect(sess, callID, peer, cfg.APIKey, cfg.Model, cfg.Voice, cfg.Prompt, cfg.InitialMessage, cfg.Record)
}

// handleCallEndedAI cleans up any active Gemini Live session associated with the call.
func (s *Server) handleCallEndedAI(sessionID, callID, peer string) {
	g := removeGeminiSession(callID)
	if g != nil {
		s.log.Info("closing active gemini live session for ended call", "call", callID, "peer", peer)
		g.Close()
		dur := 0
		if rec, ok := s.broker.GetCall(callID); ok && rec.StartedAt > 0 && rec.EndedAt != nil {
			dur = int((*rec.EndedAt - rec.StartedAt) / 1000)
		}
		s.NotifyCRMAICallEnded(peer, callID, dur, fmt.Sprintf("/api/recordings/%s.wav", callID), g.model)
	}
}

