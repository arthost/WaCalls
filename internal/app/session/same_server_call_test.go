package session

import (
	"context"
	"log/slog"
	"testing"

	"wacalls/internal/voip/call"
	"wacalls/internal/voip/core"
	"wacalls/internal/wa"

	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/types"
)

type dummySocket struct{}

var _ core.VoipSocket = dummySocket{}

func (dummySocket) OwnPN() types.JID                                 { return types.JID{} }
func (dummySocket) OwnLID() types.JID                                { return types.JID{} }
func (dummySocket) AccountDeviceIdentityNode() (waBinary.Node, bool) { return waBinary.Node{}, false }
func (dummySocket) SendNode(ctx context.Context, node waBinary.Node) error {
	return nil
}
func (dummySocket) Query(ctx context.Context, node waBinary.Node) (*waBinary.Node, error) {
	return nil, nil
}
func (dummySocket) GetUSyncDevices(ctx context.Context, jids []types.JID) ([]types.JID, error) {
	return nil, nil
}
func (dummySocket) AssertSessions(ctx context.Context, jids []types.JID, force bool) error {
	return nil
}
func (dummySocket) CreateParticipantNodes(ctx context.Context, devices []types.JID, callKey []byte, encAttrs waBinary.Attrs) ([]waBinary.Node, bool, error) {
	return nil, false, nil
}
func (dummySocket) DecryptCallKey(ctx context.Context, from types.JID, encChild *waBinary.Node) ([]byte, error) {
	return nil, nil
}
func (dummySocket) GetTCToken(ctx context.Context, jid types.JID) ([]byte, error) {
	return nil, nil
}
func (dummySocket) ResolveLIDForPN(ctx context.Context, pn types.JID) types.JID {
	return types.JID{}
}

func TestFindSessionByCall(t *testing.T) {
	mgr := &Manager{
		sessions: make(map[string]*Session),
		log:      slog.Default(),
	}

	s1 := &Session{
		id:  "sess-1",
		mgr: mgr,
		log: slog.Default(),
	}
	s1.calls = call.NewClient(wa.NewSocket(nil), s1.log, nil, 0, nil, nil)

	s2 := &Session{
		id:  "sess-2",
		mgr: mgr,
		log: slog.Default(),
	}
	s2.calls = call.NewClient(wa.NewSocket(nil), s2.log, nil, 0, nil, nil)

	mgr.sessions["sess-1"] = s1
	mgr.sessions["sess-2"] = s2

	// Initially no calls
	if s, found := mgr.FindSessionByCall("call-xyz"); found || s != nil {
		t.Fatalf("expected call not found, got %v", s)
	}

	// Register an outgoing call in s1
	c1 := call.NewOutgoingCall("call-xyz", "5511999990000@s.whatsapp.net", "5511888880000@s.whatsapp.net", core.CallMediaTypeAudio)
	cm1 := call.NewCallManager(dummySocket{}, slog.Default())
	cm1.SetCurrentCallForTest(c1)
	s1.calls.PutForTest("call-xyz", cm1)

	foundSess, ok := mgr.FindSessionByCall("call-xyz")
	if !ok || foundSess == nil || foundSess.id != "sess-1" {
		t.Fatalf("expected to find sess-1, got %v (ok=%v)", foundSess, ok)
	}

	// Session 2 should see that session 1 is the origin of this call
	origin, found := mgr.FindSessionByCall("call-xyz")
	if !found || origin.id == s2.id {
		t.Fatalf("session 2 must identify session 1 as origin")
	}
	ac, hasCall := origin.Call("call-xyz")
	if !hasCall || ac.CurrentCall().Direction != core.CallDirectionOutgoing {
		t.Fatalf("call must be identified as outgoing from session 1")
	}
}
