package call

import (
	"context"
	"log/slog"
	"testing"

	"wacalls/internal/voip/core"
	"wacalls/internal/voip/media"
	"wacalls/internal/voip/wanode"

	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/types"
)

// ssrcRelay records the last SetStreamSsrcs subscription so tests can assert the
// video SSRCs made it into the relay forwarding set. It embeds fakeRelay for the
// rest of the RelayTransport surface.
type ssrcRelay struct {
	fakeRelay
	selfSsrcs []uint32
	peerSsrcs []uint32
	resends   int
}

func (r *ssrcRelay) SetStreamSsrcs(self, peer []uint32) {
	r.selfSsrcs = append([]uint32(nil), self...)
	r.peerSsrcs = append([]uint32(nil), peer...)
}

func (r *ssrcRelay) ResendSubscriptions() { r.resends++ }

func contains(list []uint32, v uint32) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// videoCallNode builds a <call from=..><video state=N call-id=.. call-creator=..></call>
// mirroring the mid-call renegotiation stanza WhatsApp delivers via UnknownCallEvent.
func videoCallNode(state int, callID string) *waBinary.Node {
	return &waBinary.Node{
		Tag:   "call",
		Attrs: waBinary.Attrs{"from": types.NewJID("peer", "lid"), "id": "vstanza1"},
		Content: []waBinary.Node{{
			Tag: "video",
			Attrs: waBinary.Attrs{
				"call-id": callID, "call-creator": "creator@lid",
				"state": itoa(state),
			},
		}},
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

func sentVideoStates(sock *recordSock) []int {
	sock.mu.Lock()
	defer sock.mu.Unlock()
	var states []int
	for i := range sock.sent {
		for _, child := range wanode.NodeChildren(&sock.sent[i]) {
			if child.Tag == "video" {
				states = append(states, wanode.AttrInt(child.Attrs, "state", -1))
			}
		}
	}
	return states
}

func sentVideoAcks(sock *recordSock) int {
	sock.mu.Lock()
	defer sock.mu.Unlock()
	n := 0
	for i := range sock.sent {
		if sock.sent[i].Tag == "ack" &&
			wanode.AttrString(sock.sent[i].Attrs, "class") == "call" &&
			wanode.AttrString(sock.sent[i].Attrs, "type") == "video" {
			n++
		}
	}
	return n
}

// A peer-initiated upgrade (state=3) must: send the typed video ACK, derive+apply
// the peer video SSRC so the relay forwards peer video, reply with state=4
// (accept), flip MediaType to video, and fire OnPeerVideoState(Enabled).
func TestHandleVideoStateAcceptsPeerUpgrade(t *testing.T) {
	sock := &recordSock{}
	m := NewCallManager(sock, slog.Default())
	relay := &ssrcRelay{}
	m.relay = relay
	m.currentCall = activeCall()
	m.currentCall.MediaType = core.CallMediaTypeAudio
	m.peerDeviceJid = "peer@lid:0"
	m.ownDeviceJid = "me@lid:0"
	m.selfSsrc = media.GenerateSecureSsrc("c1", "me@lid:0", 0)
	m.peerSsrcs = []uint32{media.GenerateSecureSsrc("c1", "peer@lid:0", 0)}

	var fired int = -99
	m.OnPeerVideoState = func(state int) { fired = state }

	m.HandleVideoState(context.Background(), videoCallNode(3, "c1"))

	if got := sentVideoAcks(sock); got != 1 {
		t.Fatalf("expected exactly 1 typed video ACK, got %d", got)
	}
	states := sentVideoStates(sock)
	if len(states) != 1 || states[0] != 4 {
		t.Fatalf("expected one reply with state=4 (accept), got %v", states)
	}
	wantPeerVideo := media.GenerateSecureSsrc("c1", "peer@lid:0", 2)
	if m.videoPeerSsrc != wantPeerVideo {
		t.Fatalf("peer video SSRC not derived: got %d want %d", m.videoPeerSsrc, wantPeerVideo)
	}
	if !contains(relay.peerSsrcs, wantPeerVideo) {
		t.Fatalf("relay subscription must include peer video SSRC, got %v", relay.peerSsrcs)
	}
	if m.currentCall.MediaType != core.CallMediaTypeVideo {
		t.Fatal("MediaType must flip to video after accepting an upgrade")
	}
	if fired != 1 {
		t.Fatalf("expected OnPeerVideoState(Enabled=1), got %d", fired)
	}
}

// HandleVideoState on a stopped/disabled stanza just ACKs and fires Disabled; it
// must not send an accept reply.
func TestHandleVideoStateStopFiresDisabled(t *testing.T) {
	sock := &recordSock{}
	m := NewCallManager(sock, slog.Default())
	m.relay = &ssrcRelay{}
	m.currentCall = activeCall()

	var fired = -99
	m.OnPeerVideoState = func(state int) { fired = state }

	m.HandleVideoState(context.Background(), videoCallNode(6, "c1")) // Stopped

	if got := sentVideoAcks(sock); got != 1 {
		t.Fatalf("expected 1 typed video ACK, got %d", got)
	}
	if states := sentVideoStates(sock); len(states) != 0 {
		t.Fatalf("stop must not send a state reply, got %v", states)
	}
	if fired != 0 {
		t.Fatalf("expected OnPeerVideoState(Disabled=0), got %d", fired)
	}
}

// EnableLocalVideo (local operator taps camera) must derive the self video SSRC,
// extend the relay subscription with it, and send a <video state=3> upgrade
// request to the peer.
func TestEnableLocalVideoRequestsUpgrade(t *testing.T) {
	sock := &recordSock{}
	m := NewCallManager(sock, slog.Default())
	relay := &ssrcRelay{}
	m.relay = relay
	m.currentCall = activeCall()
	m.currentCall.MediaType = core.CallMediaTypeAudio
	m.ownDeviceJid = "me@lid:0"
	m.peerDeviceJid = "peer@lid:0"
	m.selfSsrc = media.GenerateSecureSsrc("c1", "me@lid:0", 0)
	m.peerSsrcs = []uint32{media.GenerateSecureSsrc("c1", "peer@lid:0", 0)}

	if err := m.EnableLocalVideo(context.Background()); err != nil {
		t.Fatalf("EnableLocalVideo: %v", err)
	}

	wantSelfVideo := media.GenerateSecureSsrc("c1", "me@lid:0", 2)
	if m.videoSelfSsrc != wantSelfVideo {
		t.Fatalf("self video SSRC not derived: got %d want %d", m.videoSelfSsrc, wantSelfVideo)
	}
	if !contains(relay.selfSsrcs, wantSelfVideo) {
		t.Fatalf("relay subscription must include self video SSRC, got %v", relay.selfSsrcs)
	}
	if m.videoRtpSession == nil {
		t.Fatal("video RtpSession must exist after enabling local video")
	}
	if !m.videoEnabled {
		t.Fatal("videoEnabled must be set")
	}
	states := sentVideoStates(sock)
	if len(states) != 1 || states[0] != 3 {
		t.Fatalf("expected one <video state=3> upgrade request, got %v", states)
	}
}

// EnableLocalVideo is a no-op error when there is no active call.
func TestEnableLocalVideoNoCall(t *testing.T) {
	m := NewCallManager(&recordSock{}, slog.Default())
	m.relay = &ssrcRelay{}
	if err := m.EnableLocalVideo(context.Background()); err == nil {
		t.Fatal("expected an error when no active call")
	}
}
