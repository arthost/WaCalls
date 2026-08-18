package call

import (
	"context"

	"wacalls/internal/voip/core"
	"wacalls/internal/voip/engine"
	"wacalls/internal/voip/media"
	"wacalls/internal/voip/signaling"
	"wacalls/internal/voip/wanode"

	waBinary "go.mau.fi/whatsmeow/binary"
)

// deriveVideoSsrcsLocked (re)computes the audio-parallel video SSRCs for the
// current call. Video uses the same JIDs as audio but counter 2 in the HKDF, so
// both sides derive matching SSRCs deterministically. Must hold m.mu.
func (m *CallManager) deriveVideoSsrcsLocked(callID, selfDeviceJid, peerDeviceJid string) {
	if selfDeviceJid == "" {
		selfDeviceJid = m.ownDeviceJid
	}
	if selfDeviceJid == "" {
		selfDeviceJid = ensureDeviceJid(m.ownCredJid())
	}
	if peerDeviceJid == "" {
		peerDeviceJid = m.peerDeviceJid
	}
	if peerDeviceJid == "" && m.acceptedByJid != "" {
		peerDeviceJid = ensureDeviceJid(m.acceptedByJid)
	}

	if selfDeviceJid != "" {
		m.ownDeviceJid = selfDeviceJid
		m.videoSelfSsrc = media.GenerateSecureSsrc(callID, selfDeviceJid, 2)
	}
	if peerDeviceJid != "" {
		m.peerDeviceJid = peerDeviceJid
		m.videoPeerSsrc = media.GenerateSecureSsrc(callID, peerDeviceJid, 2)
	}
	if m.videoSelfSsrc != 0 {
		if m.videoRtpSession == nil || m.videoRtpSession.Ssrc() != m.videoSelfSsrc {
			m.videoRtpSession = media.NewWhatsAppH264Session(m.videoSelfSsrc)
		}
		m.declareSelfSSRC(m.videoSelfSsrc)
	}
}

// rememberDeviceJidsLocked caches the device JIDs used for SSRC derivation so a
// later mid-call audio→video upgrade can derive the video SSRCs without
// re-parsing the relay participant list. Empty values are ignored. Must hold
// m.mu.
func (m *CallManager) rememberDeviceJidsLocked(selfDeviceJid, peerDeviceJid string) {
	if selfDeviceJid != "" {
		m.ownDeviceJid = selfDeviceJid
	}
	if peerDeviceJid != "" {
		m.peerDeviceJid = peerDeviceJid
	}
}

// streamSsrcsLocked returns the self/peer SSRC lists the relay should subscribe
// to. Audio is always present; video SSRCs are appended once derived so the
// relay allocates and forwards the peer's video stream too. Must hold m.mu.
func (m *CallManager) streamSsrcsLocked() (selfSsrcs, peerSsrcs []uint32) {
	if m.selfSsrc != 0 {
		selfSsrcs = append(selfSsrcs, m.selfSsrc)
	}
	if m.videoSelfSsrc != 0 {
		selfSsrcs = append(selfSsrcs, m.videoSelfSsrc)
	}
	peerSsrcs = append(peerSsrcs, m.peerSsrcs...)
	if m.videoPeerSsrc != 0 {
		peerSsrcs = append(peerSsrcs, m.videoPeerSsrc)
	}
	return selfSsrcs, peerSsrcs
}

// applyStreamSsrcsLocked pushes the current audio+video SSRC subscription set to
// the relay. Must hold m.mu.
func (m *CallManager) applyStreamSsrcsLocked() {
	selfSsrcs, peerSsrcs := m.streamSsrcsLocked()
	m.relay.SetStreamSsrcs(selfSsrcs, peerSsrcs)
	m.relay.ResendSubscriptions()
}

// sendVideoFrame packetizes one Annex-B access unit into RTP (PT 97) on the
// video SSRC and relays each fragment over the shared SRTP path. All fragments
// of a frame share ts90; the marker bit is set on the final fragment.
func (m *CallManager) sendVideoFrame(annexb []byte, ts90 uint32) error {
	m.mu.Lock()
	sess := m.videoRtpSession
	srtp := m.srtp
	relay := m.relay
	m.mu.Unlock()
	if sess == nil || srtp == nil || relay == nil || !relay.HasConnection() {
		return nil
	}
	payloads := media.PacketizeH264(annexb, 0)
	if len(payloads) == 0 {
		return nil
	}
	for i, payload := range payloads {
		marker := i == len(payloads)-1
		pkt := sess.CreatePacketAt(payload, ts90, marker)
		protected, err := srtp.Protect(pkt)
		if err != nil {
			m.log.Debug("srtp protect video error", "err", err)
			return err
		}
		relay.Broadcast(protected)
	}
	m.mu.Lock()
	m.firstVideoSent = true
	m.videoRtpPacketsSent += uint32(len(payloads))
	m.videoRtpOctetsSent += uint32(len(annexb))
	m.lastVideoRtpTs = ts90
	m.mu.Unlock()
	return nil
}

// wireVideoExtensionLocked connects the video extension's peer-frame callback to
// the CallManager's OnPeerVideo hook. Must hold m.mu — mirrors the audio wiring
// in ensureExtensionsAttachedLocked. It is a no-op when no video extension is
// registered (audio-only builds).
func (m *CallManager) wireVideoExtensionLocked() {
	if v, ok := engine.Capability[core.VideoSink](m.extensions); ok {
		v.OnPeerVideo(func(annexb []byte, ts90 uint32, keyframe bool) {
			if m.OnPeerVideo != nil {
				m.OnPeerVideo(annexb, ts90, keyframe)
			}
		})
	}
}

// FeedCapturedVideo forwards a browser-encoded Annex-B access unit into the
// video extension for packetization. No-op on audio-only calls.
func (m *CallManager) FeedCapturedVideo(annexb []byte, ts90 uint32) {
	if v, ok := engine.Capability[core.VideoSink](m.extensions); ok {
		v.FeedEncodedVideo(annexb, ts90)
	}
}

// EnableLocalVideo turns on the local video stream mid-call (audio→video
// upgrade). It derives the video SSRCs if they are not already present, extends
// the relay subscription so both video streams are forwarded, and signals the
// peer with a <video state=3> (upgrade request). The peer is expected to reply
// with state=4 (accept); frames fed via FeedCapturedVideo start flowing once the
// relay has a connection. A no-op if there is no active call.
func (m *CallManager) EnableLocalVideo(ctx context.Context) error {
	m.mu.Lock()
	call := m.currentCall
	if call == nil || call.IsEnded() {
		m.mu.Unlock()
		return &CallError{"no active call to enable video on"}
	}
	if m.videoSelfSsrc == 0 {
		m.deriveVideoSsrcsLocked(call.CallID, m.ownDeviceJid, m.peerDeviceJid)
	}
	m.applyStreamSsrcsLocked()
	m.videoEnabled = true
	call.MediaType = core.CallMediaTypeVideo
	dest := call.PeerJid
	if m.acceptedByJid != "" {
		dest = m.acceptedByJid
	}
	stanza := signaling.BuildVideoStateStanza(signaling.VideoStateParams{
		PeerJid:     wanode.MustJID(dest),
		CallID:      call.CallID,
		CallCreator: wanode.MustJID(call.CallCreator),
		State:       signaling.VideoStateUpgradeRequest,
	})
	m.mu.Unlock()

	m.relay.ResendSubscriptions()
	if err := m.sock.SendNode(ctx, stanza); err != nil {
		m.log.Error("send video upgrade request", "call_id", call.CallID, "err", err)
		return err
	}
	m.log.Info("local video enabled; upgrade requested", "call_id", call.CallID)
	return nil
}

// DisableLocalVideo turns the local camera off mid-call and tells the peer with a
// <video state=6> (stopped), so the peer's UI drops our tile instead of freezing on
// the last frame it decoded. Only the *sending* direction stops: the peer may still
// be sending video, so the relay subscription and the derived SSRCs stay in place and
// a later EnableLocalVideo can resume without renegotiating them. A no-op if there is
// no active call.
func (m *CallManager) DisableLocalVideo(ctx context.Context) error {
	m.mu.Lock()
	call := m.currentCall
	if call == nil || call.IsEnded() {
		m.mu.Unlock()
		return &CallError{"no active call to disable video on"}
	}
	m.videoEnabled = false
	callID := call.CallID
	dest := call.PeerJid
	if m.acceptedByJid != "" {
		dest = m.acceptedByJid
	}
	stanza := signaling.BuildVideoStateStanza(signaling.VideoStateParams{
		PeerJid:     wanode.MustJID(dest),
		CallID:      callID,
		CallCreator: wanode.MustJID(call.CallCreator),
		State:       signaling.VideoStateStopped,
	})
	m.mu.Unlock()

	if err := m.sock.SendNode(ctx, stanza); err != nil {
		m.log.Error("send video stop", "call_id", callID, "err", err)
		return err
	}
	m.log.Info("local video disabled", "call_id", callID)
	return nil
}

// HandleVideoState processes an inbound mid-call <video state=N> stanza. WhatsApp
// surfaces these via UnknownCallEvent (they are not a first-class whatsmeow
// event). Every video stanza MUST be answered with a typed ACK (class="call"
// type="video"); a plain ack makes WhatsApp cancel the upgrade in ~5s.
func (m *CallManager) HandleVideoState(ctx context.Context, node *waBinary.Node) {
	parsed := signaling.ParseVideoState(node)
	if !parsed.Found {
		return
	}
	// The typed ACK is required for every video stanza, regardless of state.
	_ = m.sock.SendNode(ctx, signaling.BuildVideoAck(node))

	m.mu.Lock()
	call := m.currentCall
	if call == nil || call.IsEnded() {
		m.mu.Unlock()
		return
	}
	callID := call.CallID
	creator := wanode.MustJID(call.CallCreator)
	dest := call.PeerJid
	if m.acceptedByJid != "" {
		dest = m.acceptedByJid
	}

	var (
		reply     *waBinary.Node
		fireState = -1
	)
	switch parsed.State {
	case 2, signaling.VideoStateUpgradeRequest, signaling.VideoStateUpgradeReqV2:
		// Peer sent an upgrade request (state=2/3/11). Make sure the relay forwards its video
		// stream, then send VideoStateUpgradeAccept (state=4).
		if m.videoPeerSsrc == 0 || m.videoSelfSsrc == 0 {
			m.deriveVideoSsrcsLocked(callID, m.ownDeviceJid, m.peerDeviceJid)
		}
		m.applyStreamSsrcsLocked()
		call.MediaType = core.CallMediaTypeVideo
		accept := signaling.BuildVideoStateStanza(signaling.VideoStateParams{
			PeerJid: wanode.MustJID(dest), CallID: callID, CallCreator: creator,
			State: signaling.VideoStateUpgradeAccept,
		})
		reply = &accept
		fireState = signaling.VideoStateEnabled
	case signaling.VideoStateEnabled:
		// Peer announced video enabled (state=1). Ensure SSRCs and stream subsciptions are active,
		// but do NOT send a state=4 stanza back (only the typed ACK sent above).
		if m.videoPeerSsrc == 0 || m.videoSelfSsrc == 0 {
			m.deriveVideoSsrcsLocked(callID, m.ownDeviceJid, m.peerDeviceJid)
		}
		m.applyStreamSsrcsLocked()
		call.MediaType = core.CallMediaTypeVideo
		fireState = signaling.VideoStateEnabled
	case signaling.VideoStateUpgradeAccept:
		// Our upgrade request was accepted; frames are already gated on the relay
		// connection, so nothing more to send. Surface it for the UI.
		if m.videoPeerSsrc == 0 || m.videoSelfSsrc == 0 {
			m.deriveVideoSsrcsLocked(callID, m.ownDeviceJid, m.peerDeviceJid)
		}
		m.applyStreamSsrcsLocked()
		call.MediaType = core.CallMediaTypeVideo
		fireState = signaling.VideoStateEnabled
	case signaling.VideoStateStopped, signaling.VideoStateDisabled:
		fireState = signaling.VideoStateDisabled
	case signaling.VideoStateUpgradeReject, signaling.VideoStateUpgradeCancel:
		m.videoEnabled = false
		fireState = signaling.VideoStateDisabled
	}
	m.mu.Unlock()

	if reply != nil {
		if err := m.sock.SendNode(ctx, *reply); err != nil {
			m.log.Error("send video state reply", "call_id", callID, "err", err)
		} else {
			m.relay.ResendSubscriptions()
		}
	}
	if fireState >= 0 && m.OnPeerVideoState != nil {
		m.OnPeerVideoState(fireState)
	}
	m.log.Info("video state handled", "call_id", callID, "state", parsed.State)
}
