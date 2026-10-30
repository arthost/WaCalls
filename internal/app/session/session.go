package session

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"wacalls/internal/app/events"
	"wacalls/internal/telemetry"
	"wacalls/internal/voip/call"
	"wacalls/internal/voip/codec/mlow"
	"wacalls/internal/voip/codec/opus"
	"wacalls/internal/voip/core"
	"wacalls/internal/voip/engine"
	"wacalls/internal/voip/extension/audio"
	"wacalls/internal/voip/extension/video"
	"wacalls/internal/voip/media"
	"wacalls/internal/voip/signaling"
	"wacalls/internal/voip/wanode"
	"wacalls/internal/wa"

	"github.com/mdp/qrterminal/v3"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	waevents "go.mau.fi/whatsmeow/types/events"
)

const (
	presencePulseDuration = 30 * time.Second
	presencePulseMinGap   = 1 * time.Hour
)

type Session struct {
	id   string
	name string
	mgr  *Manager
	log  *slog.Logger

	client *whatsmeow.Client
	calls  *call.Client

	bridgeMu sync.Mutex
	bridges  map[string]browserLeg

	recMu     sync.Mutex
	recorders map[string]*media.WavRecorder

	armMu           sync.Mutex
	armedRecordings map[string]bool

	lastPulse atomic.Int64
	pulsing   atomic.Bool

	mu   sync.Mutex
	auth events.AuthSnapshot
}

func newSession(mgr *Manager, id, name string, client *whatsmeow.Client) *Session {
	paired := client != nil && client.Store != nil && client.Store.ID != nil && !client.Store.ID.IsEmpty()
	s := &Session{
		id:              id,
		name:            name,
		mgr:             mgr,
		log:             mgr.log.With("session", id),
		client:          client,
		auth:            events.AuthSnapshot{State: "connecting", Paired: paired},
		bridges:         map[string]browserLeg{},
		recorders:       map[string]*media.WavRecorder{},
		armedRecordings: map[string]bool{},
	}
	s.calls = call.NewClient(wa.NewSocket(client), s.log, s.makeExtensions, mgr.maxCalls, s.wireCall, mgr.newObserver)
	client.AddEventHandler(s.handleEvent)
	return s
}

func (s *Session) makeExtensions() []engine.Extension {
	var exts []engine.Extension
	if codec, err := mlow.NewMLowCodec(mlow.DefaultCodecOptions); err == nil {
		exts = append(exts, audio.New(opus.WithFallback(codec)))
	} else {
		s.log.Warn("MLow codec unavailable; call runs without audio", "err", err)
	}
	// The video extension runs no codec (WebCodecs does that in the browser); it
	// stays dormant on audio-only calls, so it is always safe to register.
	exts = append(exts, video.New())
	return exts
}

func (s *Session) wireCall(callID string, cm *call.CallManager) {
	cm.OnIncoming = func(c *call.CallInfo) {
		raw, _ := types.ParseJID(c.PeerJid)
		pj := resolvePeerJID(context.Background(), s.client, raw)
		peer := pj.String()
		peerName := resolvePeerName(context.Background(), s.client, pj)
		photoURL := cachedPhotoURL(context.Background(), s.mgr.photos, s.id, peer)
		s.mgr.broker.UpsertCall(events.CallRecord{
			SessionID: s.id, CallID: c.CallID, Direction: "inbound", Peer: peer,
			PeerName: peerName, PeerPhotoURL: photoURL,
			StartedAt: time.Now().UnixMilli(), Status: events.StatusRinging,
		})
		s.mgr.broker.EmitIncoming(s.id, c.CallID, peer, peerName, photoURL, c.MediaType == core.CallMediaTypeVideo)
		s.mgr.tracer.StartCall(c.CallID, telemetry.CallAttrs{Session: s.id, Peer: c.PeerJid, Direction: "inbound"})
		go s.fetchPeerPhoto(pj, c.CallID)
	}
	cm.OnStateChange = func(c *call.CallInfo) {
		if c.IsEnded() {
			if b := s.getBridge(c.CallID); b != nil {
				b.SendEvent(map[string]any{"type": "call-ended", "reason": string(c.StateData.EndReason), "callId": c.CallID})
			}
			s.mgr.tracer.EndCall(c.CallID, endResult(c), string(c.StateData.EndReason), endDuration(c))
			s.removeCall(c.CallID)
			s.mgr.broker.EndCall(c.CallID, string(c.StateData.EndReason))
			return
		}
		dir := "outbound"
		if c.Direction == core.CallDirectionIncoming {
			dir = "inbound"
		}
		existing, _ := s.mgr.broker.GetCall(c.CallID)
		if existing == nil {
			s.mgr.tracer.StartCall(c.CallID, telemetry.CallAttrs{Session: s.id, Peer: c.PeerJid, Direction: dir})
		}
		st := mapStatus(c.StateData.State)
		if b := s.getBridge(c.CallID); b != nil {
			b.SendEvent(map[string]any{"type": "call-status", "status": st, "callId": c.CallID})
		}
		if st == events.StatusConnected && c.StateData.ConnectedAt != nil {
			s.mgr.tracer.MarkActive(c.CallID, c.StateData.ConnectedAt.Sub(c.CreatedAt))
		}
		if c.StateData.State == core.CallStateActive {
			s.startRecordingIfArmed(c.CallID)
		}
		rec := events.CallRecord{
			SessionID: s.id, CallID: c.CallID, Direction: dir, Peer: c.PeerJid,
			StartedAt: time.Now().UnixMilli(), Status: st,
		}
		if existing != nil {
			rec.Owner = existing.Owner
			rec.StartedAt = existing.StartedAt
			rec.Peer = existing.Peer
			rec.PeerName = existing.PeerName
			rec.PeerPhotoURL = existing.PeerPhotoURL
		}
		s.mgr.broker.UpsertCall(rec)
	}
	cm.OnEnded = func(c *call.CallInfo) {
		stats := cm.RxStats()
		s.log.Info("call audio stats", "call_id", c.CallID, "packets", stats.Packets, "gaps", stats.SpeechGaps,
			"max_gap", stats.MaxSpeechGap, "undecodable", stats.Undecodable, "relocks", stats.Relocks, "relay", stats.Relay)

		s.armMu.Lock()
		wasArmed := s.armedRecordings[c.CallID]
		delete(s.armedRecordings, c.CallID)
		s.armMu.Unlock()

		if rec := s.getRecorder(c.CallID); rec != nil {
			_ = s.StopRecording(c.CallID)
		} else if wasArmed {
			s.log.Info("call recording skipped: call was not answered", "call_id", c.CallID)
		}

		if b := s.getBridge(c.CallID); b != nil {
			b.SendEvent(map[string]any{"type": "call-ended", "reason": string(c.StateData.EndReason), "callId": c.CallID})
		}
		s.mgr.tracer.EndCall(c.CallID, endResult(c), string(c.StateData.EndReason), endDuration(c))
		s.removeCall(c.CallID)
		s.mgr.broker.EndCall(c.CallID, string(c.StateData.EndReason))
	}
	cm.OnPeerAudio = func(pcm16 []float32) {
		s.startRecordingIfArmed(callID)
		if b := s.getBridge(callID); b != nil {
			_ = b.WritePCM(pcm16)
		}
		if rec := s.getRecorder(callID); rec != nil {
			rec.WritePeer(pcm16)
		}
	}
	cm.OnPeerVideo = func(annexb []byte, ts90 uint32, keyframe bool) {
		if b := s.getBridge(callID); b != nil {
			_ = b.WriteVideo(annexb, ts90, keyframe)
		}
	}
	cm.OnPeerVideoState = func(state int) {
		s.mgr.broker.EmitVideoState(s.id, callID, state)
	}
	cm.OnQuality = func(callID string, q core.CallQuality) {
		s.mgr.broker.EmitCallQuality(s.id, callID, q)
	}
	cm.OnMark = func(callID string, mark string, elapsedMs int64) {
		s.mgr.broker.EmitCallMark(s.id, callID, mark, elapsedMs)
	}
}

func (s *Session) handleEvent(rawEvt any) {
	defer func() {
		if r := recover(); r != nil {
			s.log.Error("event handler panicked", "event", fmt.Sprintf("%T", rawEvt),
				"panic", r, "stack", string(debug.Stack()))
		}
	}()
	ctx := context.Background()
	switch evt := rawEvt.(type) {
	case *waevents.ClientOutdated:
		s.log.Error("connect failure: client outdated (405); checking web.whatsapp.com",
			"wa_version", store.GetWAVersion().String())
		s.RetryAfterOutdated(store.GetWAVersion())
	case *waevents.TemporaryBan:
		s.log.Error("connect failure: temporary ban", "code", evt.Code, "expire", evt.Expire)
	case *waevents.ConnectFailure:
		s.log.Error("connect failure", "reason", int(evt.Reason), "message", evt.Message)
	case *waevents.StreamReplaced:
		s.log.Error("stream replaced: another server or client is connected with this account's keys")
		s.setAuth(events.AuthSnapshot{State: "replaced", Paired: true})
	case *waevents.Connected:
		if id := s.client.Store.ID; id != nil {
			_ = s.mgr.store.SetJID(s.mgr.appCtx, s.id, id.String())
		}
		s.setAuth(events.AuthSnapshot{State: "open", Paired: true})
		go s.refreshPresence(s.mgr.appCtx)
	case *waevents.AppStateSyncComplete:
		if evt.Name == appstate.WAPatchCriticalBlock {
			go s.refreshPresence(s.mgr.appCtx)
		}
	case *waevents.PushNameSetting:
		go s.refreshPresence(s.mgr.appCtx)
	case *waevents.Disconnected:
		// O whatsmeow tentará reconectar automaticamente. Apenas logamos.
		s.log.Warn("WhatsApp WebSocket disconnected — auto-reconnect pending", "session", s.id)
		s.setAuth(events.AuthSnapshot{State: "connecting", Paired: true})
	case *waevents.LoggedOut:
		s.setAuth(events.AuthSnapshot{State: "logged_out", Paired: false})
	case *waevents.CallOffer:
		s.onIncomingOffer(ctx, evt)
	case *waevents.CallAccept:
		s.calls.HandleAccept(ctx, wrapCall(evt.From, evt.Data), evt.From)
	case *waevents.CallTransport:
		s.calls.HandleTransport(ctx, wrapCall(evt.From, evt.Data), evt.From)
	case *waevents.CallRelayLatency:
		s.calls.HandleRelayLatency(ctx, wrapCall(evt.From, evt.Data), evt.From)
	case *waevents.CallTerminate:
		s.calls.HandleTerminate(wrapCall(evt.From, evt.Data))
	case *waevents.CallReject:
		s.calls.HandleTerminate(wrapCall(evt.From, evt.Data))
	case *waevents.UnknownCallEvent:
		// whatsmeow has no dedicated mid-call video event; a <call><video state=N>
		// renegotiation stanza arrives here (its handleCallEvent default: branch).
		// Route it only when it actually carries a <video> child.
		if evt.Node != nil && signaling.ParseVideoState(evt.Node).Found {
			from, _ := types.ParseJID(wanode.AttrString(evt.Node.Attrs, "from"))
			s.calls.HandleVideoState(ctx, evt.Node, from)
		}
	}
}

func (s *Session) onIncomingOffer(ctx context.Context, evt *waevents.CallOffer) {
	node := wrapCall(evt.From, evt.Data)
	info := signaling.ExtractNodeInfo(node)
	if info == nil || info.CallID == "" {
		return
	}
	callID := info.CallID

	defer func() {
		if r := recover(); r != nil {
			s.log.Error("inbound offer handling panicked, dropping call", "call_id", callID,
				"panic", r, "stack", string(debug.Stack()))
			_ = s.calls.EndCall(ctx, callID, core.EndCallReasonFailed)
			s.removeCall(callID)
			s.mgr.broker.EndCall(callID, "error")
		}
	}()

	// Chamada entre duas sessões deste servidor: quem ligou já é dono deste
	// callID. Se esta sessão também registrasse a oferta, as duas disputariam o
	// mesmo registro no broker (indexado só por callID) e o card sumiria do
	// painel de quem ligou. Aqui a chamada toca só no celular.
	if origin, found := s.mgr.FindSessionByCall(callID); found && origin.id != s.id {
		if ac, ok := origin.Call(callID); ok {
			if c := ac.CurrentCall(); c != nil && c.Direction == core.CallDirectionOutgoing {
				s.log.Info("ignoring inbound offer for call placed by another session on same server", "call_id", callID, "origin_session", origin.id)
				return
			}
		}
	}

	s.calls.HandleOffer(ctx, node, evt.From)
}

func (s *Session) connect(ctx context.Context) error {
	if s.client.Store.ID != nil {
		return s.client.Connect()
	}
	return s.startPairing(ctx)
}

func (s *Session) startPairing(ctx context.Context) error {
	s.setAuth(events.AuthSnapshot{State: "connecting"})
	qrChan, err := s.client.GetQRChannel(ctx)
	if err != nil {
		return err
	}
	if err := s.client.Connect(); err != nil {
		return err
	}
	go func() {
		for evt := range qrChan {
			switch evt.Event {
			case "code":
				// The operator may have switched to a pairing code after this channel
				// was opened. Publishing the QR then would replace the code in the UI
				// with a screen they already declined.
				if s.pairingByCode() {
					continue
				}
				s.log.Info("scan the QR code to pair this session")
				qrterminal.GenerateHalfBlock(evt.Code, qrterminal.L, os.Stdout)
				s.setAuth(events.AuthSnapshot{State: "qr", QR: evt.Code})
				s.mgr.broker.EmitSessionQR(s.id, evt.Code)
			case "success":
				if id := s.client.Store.ID; id != nil {
					_ = s.mgr.store.SetJID(s.mgr.appCtx, s.id, id.String())
				}
				s.setAuth(events.AuthSnapshot{State: "open", Paired: true})
			case "timeout":
				s.setAuth(events.AuthSnapshot{State: "logged_out", Paired: false})
			}
		}
	}()
	return nil
}

// startPhonePairing links this session with an 8-digit pairing code instead of a QR
// code — the "Link with phone number instead" flow on the phone. whatsmeow only accepts
// PairPhone on a connected, unpaired client, so the QR channel is opened and the socket
// connected first; the QR events that result are suppressed while the code is in flight.
// phone must be digits only in international format (country code first).
func (s *Session) startPhonePairing(ctx context.Context, phone string) (string, error) {
	if s.client.Store.ID != nil {
		return "", fmt.Errorf("session already paired")
	}
	if !s.client.IsConnected() {
		if err := s.startPairing(ctx); err != nil {
			return "", err
		}
		deadline := time.Now().Add(8 * time.Second)
		for !s.client.IsConnected() && time.Now().Before(deadline) {
			time.Sleep(100 * time.Millisecond)
		}
		if !s.client.IsConnected() {
			return "", fmt.Errorf("websocket not connected; cannot request pairing code")
		}
	}
	// Display name must follow "Browser (OS)" format to prevent WhatsApp 400 bad-request
	code, err := s.client.PairPhone(ctx, phone, true, whatsmeow.PairClientChrome, "Chrome (Linux)")
	if err != nil {
		return "", err
	}
	s.setAuth(events.AuthSnapshot{State: "pair_code", Code: code})
	s.log.Info("pairing code issued", "session", s.id)
	return code, nil
}

// pairingByCode reports whether a phone-code link is in flight, which makes the QR
// events from the same connection stale.
func (s *Session) pairingByCode() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.auth.Code != ""
}

func (s *Session) setAuth(a events.AuthSnapshot) {
	s.mu.Lock()
	s.auth = a
	s.mu.Unlock()
	s.mgr.broker.EmitAuthState(s.id, a)
	s.mgr.broker.EmitSessionList(s.mgr.Infos())
}

func (s *Session) Info() events.SessionInfo {
	s.mu.Lock()
	a := s.auth
	s.mu.Unlock()
	jid := ""
	if id := s.client.Store.ID; id != nil {
		jid = id.String()
	}
	return events.SessionInfo{ID: s.id, Name: s.name, JID: jid, State: a.State, Paired: a.Paired || jid != "", QR: a.QR, Code: a.Code}
}

func (s *Session) getBridge(callID string) browserLeg {
	s.bridgeMu.Lock()
	defer s.bridgeMu.Unlock()
	return s.bridges[callID]
}

func (s *Session) setBridge(callID string, b browserLeg) {
	s.bridgeMu.Lock()
	old := s.bridges[callID]
	if _, live := s.calls.Get(callID); !live {
		s.bridgeMu.Unlock()
		b.Close()
		return
	}
	s.bridges[callID] = b
	s.bridgeMu.Unlock()
	if old != nil {
		old.Close()
	}
}

func (s *Session) removeCall(callID string) {
	_ = s.StopRecording(callID)
	s.bridgeMu.Lock()
	b := s.bridges[callID]
	delete(s.bridges, callID)
	s.bridgeMu.Unlock()
	if b != nil {
		b.Close()
	}
	s.calls.Remove(callID)
}

func (s *Session) Call(callID string) (*call.CallManager, bool) {
	return s.calls.Get(callID)
}

func (s *Session) StartRecording(callID string) error {
	cm, ok := s.calls.Get(callID)
	if ok && cm.CurrentCall() != nil && cm.CurrentCall().StateData.State == core.CallStateActive {
		return s.startRecordingWav(callID)
	}
	s.armMu.Lock()
	if s.armedRecordings == nil {
		s.armedRecordings = make(map[string]bool)
	}
	s.armedRecordings[callID] = true
	s.armMu.Unlock()
	s.log.Info("call recording armed; will begin when answered", "call_id", callID)
	return nil
}

func (s *Session) startRecordingIfArmed(callID string) {
	s.armMu.Lock()
	armed := s.armedRecordings[callID]
	s.armMu.Unlock()
	if !armed {
		return
	}
	if s.getRecorder(callID) != nil {
		return
	}
	if err := s.startRecordingWav(callID); err != nil {
		s.log.Error("failed to start armed recording", "call_id", callID, "err", err)
	} else {
		s.log.Info("armed recording started", "call_id", callID)
	}
}

func (s *Session) startRecordingWav(callID string) error {
	s.recMu.Lock()
	defer s.recMu.Unlock()
	if s.recorders[callID] != nil {
		return nil
	}
	name := recordingFileName(callID)
	if name == "" {
		return fmt.Errorf("call id %q is not usable as a file name", callID)
	}
	dir := s.mgr.RecordingsDir
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	path := filepath.Join(dir, name)
	rec, err := media.NewWavRecorder(path)
	if err != nil {
		return err
	}
	s.recorders[callID] = rec
	s.mgr.broker.EmitRecordState(s.id, callID, true)
	return nil
}

// recordingFileName maps a call ID to a safe "<id>.wav" leaf, keeping only the
// characters WhatsApp actually uses in call IDs. Returns "" when nothing is left.
func recordingFileName(callID string) string {
	var b strings.Builder
	for _, r := range callID {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return ""
	}
	return b.String() + ".wav"
}

func (s *Session) StopRecording(callID string) error {
	s.armMu.Lock()
	delete(s.armedRecordings, callID)
	s.armMu.Unlock()

	s.recMu.Lock()
	rec := s.recorders[callID]
	delete(s.recorders, callID)
	s.recMu.Unlock()
	if rec != nil {
		err := rec.Close()
		s.mgr.broker.EmitRecordState(s.id, callID, false)
		return err
	}
	return nil
}

func (s *Session) sendPresenceTimeout(ctx context.Context, state types.Presence) error {
	ctxTimeout, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return s.SendPresence(ctxTimeout, state)
}

func (s *Session) refreshPresence(ctx context.Context) {
	if time.Since(time.Unix(0, s.lastPulse.Load())) >= presencePulseMinGap {
		s.presencePulse(ctx)
		return
	}
	if s.pulsing.Load() {
		return
	}
	if err := s.sendPresenceTimeout(ctx, types.PresenceUnavailable); err != nil {
		s.log.Warn("failed to send presence unavailable", "err", err)
	}
}

func (s *Session) presencePulse(ctx context.Context) {
	if !s.pulsing.CompareAndSwap(false, true) {
		return
	}
	defer s.pulsing.Store(false)
	if err := s.sendPresenceTimeout(ctx, types.PresenceAvailable); err != nil {
		if errors.Is(err, whatsmeow.ErrNoPushName) {
			s.log.Debug("presence pulse deferred until pushname arrives")
		} else {
			s.log.Warn("presence pulse failed", "err", err)
		}
		return
	}
	s.lastPulse.Store(time.Now().UnixNano())
	s.log.Info("presence pulse sent (available 30s)")
	select {
	case <-ctx.Done():
		return
	case <-time.After(presencePulseDuration):
	}
	if err := s.sendPresenceTimeout(ctx, types.PresenceUnavailable); err != nil {
		s.log.Warn("presence pulse: failed to return to unavailable", "err", err)
	} else {
		s.log.Debug("presence pulse ended (returned to unavailable)")
	}
}

func (s *Session) getRecorder(callID string) *media.WavRecorder {
	s.recMu.Lock()
	defer s.recMu.Unlock()
	return s.recorders[callID]
}

func (s *Session) terminateCall(callID string, reason core.EndCallReason) {
	_ = s.calls.EndCall(context.Background(), callID, reason)
}

func (s *Session) teardownAllCalls() {
	for _, cm := range s.calls.Drain() {
		_ = cm.EndCall(context.Background(), core.EndCallReasonUserEnded)
	}
	s.bridgeMu.Lock()
	bridges := s.bridges
	s.bridges = map[string]browserLeg{}
	s.bridgeMu.Unlock()
	for _, b := range bridges {
		if b != nil {
			b.Close()
		}
	}
}

func (s *Session) replaceClient(client *whatsmeow.Client) {
	s.teardownAllCalls()
	s.client.Disconnect()
	s.client = client
	client.AddEventHandler(s.handleEvent)
}

func (s *Session) shutdown() {
	s.teardownAllCalls()
	s.client.Disconnect()
}

func (s *Session) SendPresence(ctx context.Context, state types.Presence) error {
	s.mu.Lock()
	client := s.client
	s.mu.Unlock()
	if client == nil || !client.IsConnected() {
		return fmt.Errorf("session not connected")
	}
	return client.SendPresence(ctx, state)
}

func (s *Session) OwnPhone() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.client == nil || s.client.Store == nil || s.client.Store.ID == nil {
		return ""
	}
	return s.client.Store.ID.User
}

func endResult(c *call.CallInfo) string {
	if c.StateData.ConnectedAt != nil {
		return "completed"
	}
	return "failed"
}

func endDuration(c *call.CallInfo) time.Duration {
	if c.StateData.EndedAt != nil {
		return c.StateData.EndedAt.Sub(c.CreatedAt)
	}
	return 0
}

func mapStatus(state core.CallState) events.CallStatus {
	switch state {
	case core.CallStateActive:
		return events.StatusConnected
	case core.CallStateReconnecting:
		return events.StatusReconnecting
	case core.CallStateEnded:
		return events.StatusEnded
	case core.CallStateInitiating:
		return events.StatusStarting
	default:
		return events.StatusRinging
	}
}
