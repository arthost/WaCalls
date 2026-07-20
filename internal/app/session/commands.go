package session

import (
	"context"
	"errors"
	"fmt"
	"time"

	"wacalls/internal/voip/call"
	"wacalls/internal/voip/core"

	"go.mau.fi/whatsmeow/types"
)

var ErrTooManyCalls = errors.New("max concurrent calls")

type StartedCall struct{ CallID, Peer, PeerName, PeerPhotoURL string }

func (s *Session) ID() string { return s.id }

func (s *Session) IsPaired() bool { return s.client.Store.ID != nil }

func (s *Session) HasCall(callID string) bool {
	_, ok := s.calls.Get(callID)
	return ok
}

func (s *Session) StartCall(ctx context.Context, phone string) (StartedCall, error) {
	if max := s.mgr.maxCalls; max > 0 && s.calls.Count() >= max {
		return StartedCall{}, ErrTooManyCalls
	}

	// Garante que o WebSocket do WhatsApp está ativo antes de iniciar a chamada.
	// O whatsmeow pode estar em reconexão silenciosa mesmo com state="open".
	if !s.client.IsConnected() {
		s.log.Warn("WhatsApp WebSocket not connected before call — attempting reconnect", "session", s.id)
		if err := s.client.Connect(); err != nil {
			return StartedCall{}, fmt.Errorf("websocket reconnect failed: %w", err)
		}
		// Aguarda o cliente estabelecer a conexão (máx 5 s)
		deadline := time.Now().Add(5 * time.Second)
		for !s.client.IsConnected() && time.Now().Before(deadline) {
			time.Sleep(200 * time.Millisecond)
		}
		if !s.client.IsConnected() {
			return StartedCall{}, fmt.Errorf("websocket not connected after reconnect attempt")
		}
	}

	peer := types.NewJID(phone, types.DefaultUserServer)
	callID, err := s.calls.StartCall(ctx, peer)
	if err != nil {
		return StartedCall{}, err
	}
	return StartedCall{
		CallID:       callID,
		Peer:         peer.String(),
		PeerName:     resolvePeerName(ctx, s.client, peer),
		PeerPhotoURL: cachedPhotoURL(ctx, s.mgr.photos, s.id, peer.String()),
	}, nil
}

func (s *Session) FetchCallPhoto(callID, peer string) {
	jid, err := types.ParseJID(peer)
	if err != nil {
		return
	}
	go s.fetchPeerPhoto(jid, callID)
}

func (s *Session) AcceptCall(ctx context.Context, callID string) error {
	return s.calls.AcceptCall(ctx, callID)
}

func (s *Session) RejectCall(ctx context.Context, callID string) error {
	err := s.calls.RejectCall(ctx, callID, core.EndCallReasonDeclined)
	var invalid *call.InvalidTransition
	if errors.As(err, &invalid) {
		return err
	}
	s.removeCall(callID)
	return nil
}

func (s *Session) EndCall(ctx context.Context, callID string) error {
	err := s.calls.EndCall(ctx, callID, core.EndCallReasonUserEnded)
	s.removeCall(callID)
	return err
}

func (s *Session) AttachBrowser(callID, offerSDP string) (string, error) {
	cm, ok := s.calls.Get(callID)
	if !ok {
		return "", fmt.Errorf("no such call %s", callID)
	}
	bridge, answer, err := NewBridge(s.mgr.webrtcAPI, offerSDP, s.log)
	if err != nil {
		return "", err
	}
	bridge.OnBrowserPCM = func(pcm []float32) { cm.FeedCapturedPCM(pcm) }
	bridge.OnTerminalICE = func() { go s.terminateCall(callID, core.EndCallReasonUserEnded) }
	s.setBridge(callID, bridge)
	return answer, nil
}
