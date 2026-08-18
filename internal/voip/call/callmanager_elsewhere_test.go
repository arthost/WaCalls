package call

import (
	"context"
	"testing"

	"wacalls/internal/voip/wanode"

	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/types"
)

func acceptNode(callID string, from types.JID) *waBinary.Node {
	return &waBinary.Node{
		Tag:   "call",
		Attrs: waBinary.Attrs{"from": from, "id": "ACCEPT1"},
		Content: []waBinary.Node{{
			Tag:   "accept",
			Attrs: waBinary.Attrs{"call-id": callID, "call-creator": from.String()},
		}},
	}
}

func terminateNode(callID string, from types.JID, reason string) *waBinary.Node {
	return &waBinary.Node{
		Tag:   "call",
		Attrs: waBinary.Attrs{"from": from},
		Content: []waBinary.Node{{
			Tag:   "terminate",
			Attrs: waBinary.Attrs{"call-id": callID, "reason": reason},
		}},
	}
}

// elsewhereTargets collects the device JIDs of every accepted_elsewhere terminate the
// manager pushed to the socket, one slice per stanza, so a test can assert both how
// many fan-outs happened and who each one rang off.
func elsewhereTargets(sock *recordSock) [][]string {
	sock.mu.Lock()
	defer sock.mu.Unlock()
	var out [][]string
	for i := range sock.sent {
		for _, child := range wanode.NodeChildren(&sock.sent[i]) {
			if child.Tag != "terminate" || wanode.AttrString(child.Attrs, "reason") != "accepted_elsewhere" {
				continue
			}
			targets := []string{}
			for _, dest := range wanode.NodeChildren(&child) {
				if dest.Tag != "destination" {
					continue
				}
				for _, to := range wanode.NodeChildren(&dest) {
					if to.Tag == "to" {
						targets = append(targets, wanode.AttrString(to.Attrs, "jid"))
					}
				}
			}
			out = append(out, targets)
		}
	}
	return out
}

func ringingWithDevices(t *testing.T, sock *recordSock, devices ...types.JID) *CallManager {
	t.Helper()
	cm := ringingManager(t, sock)
	cm.mu.Lock()
	cm.calleeDevices = devices
	cm.mu.Unlock()
	sock.mu.Lock()
	// Drop the preaccept HandleOffer already sent, so the assertions below only see
	// what the accept path produced.
	sock.sent = nil
	sock.mu.Unlock()
	return cm
}

func TestFirstAcceptRingsOffSiblingDevices(t *testing.T) {
	sock := &recordSock{}
	answering := types.JID{User: "5511999990000", Device: 33, Server: types.DefaultUserServer}
	sibling := types.JID{User: "5511999990000", Device: 7, Server: types.DefaultUserServer}
	cm := ringingWithDevices(t, sock, answering, sibling)

	cm.HandleCallAccept(context.Background(), acceptNode("CALL1", answering), answering)

	fanouts := elsewhereTargets(sock)
	if len(fanouts) != 1 {
		t.Fatalf("want exactly one accepted_elsewhere stanza, got %d: %v", len(fanouts), fanouts)
	}
	if len(fanouts[0]) != 1 || fanouts[0][0] != sibling.String() {
		t.Fatalf("accepted_elsewhere must target only the non-answering device %q, got %v",
			sibling.String(), fanouts[0])
	}
}

func TestFirstAcceptWithNoSiblingsSendsNoFanout(t *testing.T) {
	sock := &recordSock{}
	answering := types.JID{User: "5511999990000", Device: 33, Server: types.DefaultUserServer}
	cm := ringingWithDevices(t, sock, answering)

	cm.HandleCallAccept(context.Background(), acceptNode("CALL1", answering), answering)

	if fanouts := elsewhereTargets(sock); len(fanouts) != 0 {
		t.Fatalf("single-device callee needs no accepted_elsewhere, got %v", fanouts)
	}
}

func TestAcceptFromSecondDeviceIsIgnored(t *testing.T) {
	sock := &recordSock{}
	answering := types.JID{User: "5511999990000", Device: 33, Server: types.DefaultUserServer}
	sibling := types.JID{User: "5511999990000", Device: 7, Server: types.DefaultUserServer}
	cm := ringingWithDevices(t, sock, answering, sibling)

	cm.HandleCallAccept(context.Background(), acceptNode("CALL1", answering), answering)
	cm.mu.Lock()
	peerSsrc := firstSsrc(cm.peerSsrcs)
	cm.mu.Unlock()
	sock.mu.Lock()
	sock.sent = nil
	sock.mu.Unlock()

	// The sibling picks up (or races our fan-out) after media is already flowing.
	cm.HandleCallAccept(context.Background(), acceptNode("CALL1", sibling), sibling)

	cm.mu.Lock()
	gotAccepted := cm.acceptedByJid
	gotSsrc := firstSsrc(cm.peerSsrcs)
	cm.mu.Unlock()
	if gotAccepted != answering.String() {
		t.Errorf("acceptedByJid = %q, want it pinned to the first device %q", gotAccepted, answering.String())
	}
	if gotSsrc != peerSsrc {
		t.Errorf("peer ssrc rekeyed to %d, want it left at %d", gotSsrc, peerSsrc)
	}
	sock.mu.Lock()
	n := len(sock.sent)
	sock.mu.Unlock()
	if n != 0 {
		t.Errorf("ignored accept sent %d stanzas, want none: %v", n, sock.sentInnerTags())
	}
}

func TestAcceptRetryFromSameDeviceDoesNotRefanout(t *testing.T) {
	sock := &recordSock{}
	answering := types.JID{User: "5511999990000", Device: 33, Server: types.DefaultUserServer}
	sibling := types.JID{User: "5511999990000", Device: 7, Server: types.DefaultUserServer}
	cm := ringingWithDevices(t, sock, answering, sibling)

	// A retransmitted accept from the SAME device must still be processed (it is the
	// only chance to repair a call key we could not decrypt the first time), but the
	// sibling has already been rung off and must not be terminated twice.
	cm.HandleCallAccept(context.Background(), acceptNode("CALL1", answering), answering)
	cm.HandleCallAccept(context.Background(), acceptNode("CALL1", answering), answering)

	if fanouts := elsewhereTargets(sock); len(fanouts) != 1 {
		t.Fatalf("want exactly one accepted_elsewhere across both accepts, got %d: %v", len(fanouts), fanouts)
	}
}

func TestTerminateFromNonAnsweringDeviceIsIgnored(t *testing.T) {
	sock := &recordSock{}
	answering := types.JID{User: "5511999990000", Device: 33, Server: types.DefaultUserServer}
	sibling := types.JID{User: "5511999990000", Device: 7, Server: types.DefaultUserServer}
	cm := ringingWithDevices(t, sock, answering, sibling)
	cm.mu.Lock()
	cm.acceptedByJid = answering.String()
	cm.mu.Unlock()

	// The sibling kept ringing until it timed out; its late terminate must not tear
	// down the call that is live on the answering device.
	cm.HandleCallTerminate(terminateNode("CALL1", sibling, "timeout"))

	if c := cm.CurrentCall(); c == nil || c.IsEnded() {
		t.Fatal("call must survive a terminate from a device that never answered")
	}

	cm.HandleCallTerminate(terminateNode("CALL1", answering, "user_ended"))
	if c := cm.CurrentCall(); c == nil || !c.IsEnded() {
		t.Fatal("terminate from the answering device must end the call")
	}
}

func TestTerminateBeforeAcceptEndsCall(t *testing.T) {
	sock := &recordSock{}
	peer := types.JID{User: "5511999990000", Server: types.DefaultUserServer}
	cm := ringingWithDevices(t, sock)

	// Nobody answered yet, so there is no answering device to compare against and a
	// cancel from any of the callee's devices has to end the call.
	cm.HandleCallTerminate(terminateNode("CALL1", peer, "timeout"))
	if c := cm.CurrentCall(); c == nil || !c.IsEnded() {
		t.Fatal("terminate on a ringing call must end it")
	}
}
