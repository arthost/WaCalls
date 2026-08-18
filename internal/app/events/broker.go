// Package events is the worker->front seam: the SSE broker that fans call and
// session events out to connected operator browsers, the live call registry it
// tracks, and the outbound webhook dispatcher. It depends only on voip/core;
// the session worker and the httpapi front both import it.
package events

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"wacalls/internal/voip/core"
)

type AuthSnapshot struct {
	State  string `json:"state"`
	Paired bool   `json:"paired"`
	QR     string `json:"qr,omitempty"`
	// Code is the 8-digit pairing code, set only while a link-by-phone-number flow is
	// in flight. It is an alternative to QR, never shown alongside one.
	Code string `json:"code,omitempty"`
}

type SessionInfo struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	JID    string `json:"jid"`
	State  string `json:"state"`
	Paired bool   `json:"paired"`
	QR     string `json:"qr,omitempty"`
	Code   string `json:"code,omitempty"`
}

type subscriber struct {
	clientID string
	ch       chan []byte
	kick     chan struct{}
	kickOnce sync.Once
}

type Broker struct {
	mu       sync.RWMutex
	subs     map[*subscriber]struct{}
	calls    map[string]*CallRecord
	records  core.CallRecordStore
	webhooks *webhookDispatcher
	log      *slog.Logger

	SnapshotFn func() []any
}

func NewBroker(records core.CallRecordStore, log *slog.Logger) *Broker {
	if log == nil {
		log = slog.Default()
	}
	return &Broker{
		subs:    map[*subscriber]struct{}{},
		calls:   map[string]*CallRecord{},
		records: records,
		log:     log,
	}
}

// EnableWebhooks starts an outbound webhook dispatcher for the given URL, if non-empty,
// and reports whether delivery was enabled. The dispatcher runs until ctx is cancelled.
func (b *Broker) EnableWebhooks(ctx context.Context, url, secret string) bool {
	if url == "" {
		return false
	}
	b.webhooks = newWebhookDispatcher(url, secret, b.log)
	go b.webhooks.run(ctx)
	return true
}

func (b *Broker) subscribe(clientID string) *subscriber {
	s := &subscriber{clientID: clientID, ch: make(chan []byte, 32), kick: make(chan struct{})}
	b.mu.Lock()
	b.subs[s] = struct{}{}
	b.mu.Unlock()
	return s
}

func (b *Broker) unsubscribe(s *subscriber) {
	b.mu.Lock()
	delete(b.subs, s)
	b.mu.Unlock()
	close(s.ch)
}

func (b *Broker) broadcast(ev any) {
	data, err := json.Marshal(ev)
	if err != nil {
		return
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	for s := range b.subs {
		select {
		case s.ch <- data:
		default:
			s.kickOnce.Do(func() {
				b.log.Warn("sse subscriber lagging, kicking for resync", "client_id", s.clientID)
				close(s.kick)
			})
		}
	}
}

func (b *Broker) EmitAuthState(sessionID string, a AuthSnapshot) {
	b.broadcast(map[string]any{
		"type": "auth-state", "sessionId": sessionID,
		"paired": a.Paired, "state": a.State, "qr": a.QR,
	})
}

func (b *Broker) EmitSessionList(sessions []SessionInfo) {
	b.broadcast(map[string]any{"type": "session-list", "sessions": sessions})
}

func (b *Broker) EmitSessionQR(sessionID, qr string) {
	b.broadcast(map[string]any{"type": "session-qr", "sessionId": sessionID, "qr": qr})
}

func (b *Broker) EmitIncoming(sessionID, id, peer, peerName, peerPhotoURL string, isVideo bool) {
	b.broadcast(map[string]any{
		"type": "incoming", "sessionId": sessionID, "id": id, "peer": peer,
		"peerName": peerName, "peerPhotoUrl": peerPhotoURL, "isVideo": isVideo,
		"offeredAt": time.Now().UnixMilli(),
	})
}

func (b *Broker) EmitIncomingClaimed(sessionID, id, owner string) {
	b.broadcast(map[string]any{"type": "incoming-claimed", "sessionId": sessionID, "id": id, "owner": owner})
}

// EmitGroupCall broadcasts a group call initiation or invitation event
func (b *Broker) EmitGroupCall(sessionID, callID string, phones []string, isVideo bool, groupJID string) {
	b.broadcast(map[string]any{
		"type": "group-call", "sessionId": sessionID, "id": callID,
		"phones": phones, "isVideo": isVideo, "groupJid": groupJID,
		"offeredAt": time.Now().UnixMilli(),
	})
}

// EmitCallControl broadcasts transient call control signals (screen share, hand raise, emoji reaction)
func (b *Broker) EmitCallControl(sessionID, callID, action, participant, emoji string, extra map[string]any) {
	ev := map[string]any{
		"type": "call-control", "sessionId": sessionID, "id": callID,
		"action": action, "participant": participant, "emoji": emoji,
	}
	for k, v := range extra {
		ev[k] = v
	}
	b.broadcast(ev)
}

// EmitCallLobby broadcasts lobby status changes (participant waiting, admitted, rejected)
func (b *Broker) EmitCallLobby(sessionID, callID, participant, action string) {
	b.broadcast(map[string]any{
		"type": "call-lobby", "sessionId": sessionID, "id": callID,
		"participant": participant, "action": action,
		"timestamp": time.Now().UnixMilli(),
	})
}

// EmitCallParticipants broadcasts participant list changes (added, removed, rering)
func (b *Broker) EmitCallParticipants(sessionID, callID, participant, action string, phones []string) {
	b.broadcast(map[string]any{
		"type": "call-participants", "sessionId": sessionID, "id": callID,
		"participant": participant, "action": action, "phones": phones,
		"timestamp": time.Now().UnixMilli(),
	})
}

// EmitCallQuality broadcasts a live per-call reception-quality sample (RTT, jitter, loss) derived
// from inbound RTCP. It is a transient live-only signal: it never touches CallRecord, persistence,
// or webhooks, so the client's call card can render it without polluting the history contract.
func (b *Broker) EmitCallQuality(sessionID, callID string, q core.CallQuality) {
	b.broadcast(map[string]any{
		"type": "call-quality", "sessionId": sessionID, "id": callID,
		"rttMs": q.RttMs, "jitterMs": q.JitterMs, "lossFraction": q.LossFraction, "hasRtt": q.HasRtt,
	})
}

// EmitCallMark broadcasts a connection-setup phase mark (STUN/ICE/DTLS/SCTP/first-packet) with its
// elapsed time since call start. Like call-quality it is a transient live-only signal, never
// persisted, feeding the client's connection timeline.
func (b *Broker) EmitCallMark(sessionID, callID, mark string, elapsedMs int64) {
	b.broadcast(map[string]any{
		"type": "call-mark", "sessionId": sessionID, "id": callID,
		"mark": mark, "elapsedMs": elapsedMs,
	})
}

// EmitVideoState broadcasts a mid-call video negotiation state change (the peer
// enabled/disabled their camera, or accepted/rejected our upgrade request). The
// numeric state is a signaling.VideoState* constant. Transient live-only signal;
// never persisted — feeds the client's camera/video toggle UI.
func (b *Broker) EmitVideoState(sessionID, callID string, state int) {
	b.broadcast(map[string]any{
		"type": "call-video-state", "sessionId": sessionID, "id": callID,
		"state": state,
	})
}

func (b *Broker) EmitHoldState(sessionID, callID string, onHold bool) {
	b.broadcast(map[string]any{
		"type": "call-hold-state", "sessionId": sessionID, "id": callID,
		"onHold": onHold,
	})
}

func (b *Broker) EmitRecordState(sessionID, callID string, recording bool) {
	b.broadcast(map[string]any{
		"type": "call-record-state", "sessionId": sessionID, "id": callID,
		"recording": recording,
	})
}

func (b *Broker) EmitTransfer(sessionID, callID, toOwner, fromOwner string) {
	b.broadcast(map[string]any{
		"type": "call-transfer", "sessionId": sessionID, "id": callID,
		"toOwner": toOwner, "fromOwner": fromOwner,
	})
}

func (b *Broker) ServeSSE(w http.ResponseWriter, r *http.Request, clientID string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	sub := b.subscribe(clientID)
	defer b.unsubscribe(sub)

	if b.SnapshotFn != nil {
		for _, ev := range b.SnapshotFn() {
			writeSSE(w, flusher, ev)
		}
	}
	writeSSE(w, flusher, map[string]any{"type": "call-list", "calls": b.callList()})

	keepalive := time.NewTicker(10 * time.Second)
	defer keepalive.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-sub.kick:
			return
		case data := <-sub.ch:
			if _, err := w.Write(append(append([]byte("data: "), data...), '\n', '\n')); err != nil {
				return
			}
			flusher.Flush()
		case <-keepalive.C:
			// A data event instead of an SSE comment so the client can track
			// stream liveness and force a reconnect when pings stop arriving.
			writeSSE(w, flusher, map[string]any{"type": "ping"})
		}
	}
}

func writeSSE(w http.ResponseWriter, f http.Flusher, ev any) {
	data, _ := json.Marshal(ev)
	_, _ = w.Write(append(append([]byte("data: "), data...), '\n', '\n'))
	f.Flush()
}
