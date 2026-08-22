package session

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"wacalls/internal/app/assets"
	"wacalls/internal/voip/call"
	"wacalls/internal/voip/core"

	"github.com/coder/websocket"
	"go.mau.fi/whatsmeow/types"
)

var ErrTooManyCalls = errors.New("max concurrent calls")
var ErrWebSocketDisconnected = errors.New("whatsapp websocket disconnected")

type StartedCall struct{ CallID, Peer, PeerName, PeerPhotoURL string }

func (s *Session) ID() string { return s.id }

func (s *Session) IsPaired() bool { return s.client.Store.ID != nil }

func (s *Session) HasCall(callID string) bool {
	_, ok := s.calls.Get(callID)
	return ok
}

func (s *Session) StartCall(ctx context.Context, phone string, video bool) (StartedCall, error) {
	if max := s.mgr.maxCalls; max > 0 && s.calls.Count() >= max {
		return StartedCall{}, ErrTooManyCalls
	}

	reconnect := func() error {
		s.client.Disconnect()
		if err := s.client.Connect(); err != nil {
			return fmt.Errorf("%w: reconnect failed: %v", ErrWebSocketDisconnected, err)
		}
		if err := waitConnected(ctx, s.client.IsConnected); err != nil {
			return fmt.Errorf("%w: %v", ErrWebSocketDisconnected, err)
		}
		return nil
	}
	ensureConnected := func() error {
		if !s.client.IsConnected() {
			s.log.Warn("WhatsApp WebSocket disconnected — attempting reconnect", "session", s.id)
			return reconnect()
		}
		return nil
	}

	if err := ensureConnected(); err != nil {
		return StartedCall{}, err
	}

	peer := types.NewJID(phone, types.DefaultUserServer)
	callID, err := s.calls.StartCall(ctx, peer, video)
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "websocket not connected") {
		s.log.Warn("Call offer failed with websocket disconnected — forcing reconnect & retry", "session", s.id)
		if reconnectErr := reconnect(); reconnectErr != nil {
			return StartedCall{}, reconnectErr
		}
		callID, err = s.calls.StartCall(ctx, peer, video)
	}

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

func waitConnected(ctx context.Context, connected func() bool) error {
	if connected() {
		return nil
	}
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return errors.New("websocket not connected after reconnect attempt")
		case <-ticker.C:
			if connected() {
				return nil
			}
		}
	}
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

func (s *Session) EnableVideo(ctx context.Context, callID string) error {
	return s.calls.EnableVideo(ctx, callID)
}

// DisableVideo stops the outbound camera stream and tells the peer, so the peer's UI
// drops our tile instead of freezing on the last frame it decoded. Inbound video is
// untouched — the peer may still be sending.
func (s *Session) DisableVideo(ctx context.Context, callID string) error {
	return s.calls.DisableVideo(ctx, callID)
}

func (s *Session) HoldCall(ctx context.Context, callID string) error {
	diskPath := filepath.Join(s.mgr.DataDir, "hold-music.wav")
	music := assets.LoadHoldMusic(diskPath)
	err := s.calls.HoldCall(ctx, callID, music)
	if err == nil {
		s.mgr.broker.EmitHoldState(s.id, callID, true)
	}
	return err
}

func (s *Session) ResumeCall(ctx context.Context, callID string) error {
	err := s.calls.ResumeCall(ctx, callID)
	if err == nil {
		s.mgr.broker.EmitHoldState(s.id, callID, false)
	}
	return err
}

func (s *Session) TransferCall(ctx context.Context, callID, toOwner, fromOwner string) error {
	if err := s.HoldCall(ctx, callID); err != nil {
		return err
	}
	s.mgr.broker.EmitTransfer(s.id, callID, toOwner, fromOwner)
	return nil
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
	bridge.OnBrowserPCM = func(pcm []float32) {
		cm.FeedCapturedPCM(pcm)
		if rec := s.getRecorder(callID); rec != nil {
			rec.WriteOperator(pcm)
		}
	}
	bridge.OnBrowserVideo = func(annexb []byte, ts90 uint32) { cm.FeedCapturedVideo(annexb, ts90) }
	bridge.OnTerminalICE = func() { go s.terminateCall(callID, core.EndCallReasonUserEnded) }
	s.setBridge(callID, bridge)
	_ = cm.Resume()
	s.mgr.broker.EmitHoldState(s.id, callID, false)
	return answer, nil
}

// AttachBrowserWS makes an already-upgraded WebSocket the operator leg of a call,
// replacing whatever transport was attached before (so an operator can be handed a
// transferred call the same way as over WebRTC). It blocks until the socket closes:
// the caller is the HTTP handler that accepted the upgrade, and returning from that
// handler would tear the hijacked connection down.
func (s *Session) AttachBrowserWS(callID string, conn *websocket.Conn) error {
	cm, ok := s.calls.Get(callID)
	if !ok {
		return fmt.Errorf("no such call %s", callID)
	}
	bridge := NewWSBridge(conn, s.log)
	bridge.OnBrowserPCM = func(pcm []float32) {
		cm.FeedCapturedPCM(pcm)
		if rec := s.getRecorder(callID); rec != nil {
			rec.WriteOperator(pcm)
		}
	}
	bridge.OnTerminal = func() { go s.terminateCall(callID, core.EndCallReasonUserEnded) }
	s.setBridge(callID, bridge)
	_ = cm.Resume()
	s.mgr.broker.EmitHoldState(s.id, callID, false)
	bridge.ReadLoop()
	return nil
}
