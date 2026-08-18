package signaling

import (
	"testing"

	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/types"
)

func TestBuildTerminateElsewhereStanza(t *testing.T) {
	peer := types.JID{User: "5511999999999", Server: types.DefaultUserServer}
	creator := types.JID{User: "5511888888888", Server: types.DefaultUserServer}
	devices := []types.JID{
		{User: "5511999999999", Device: 1, Server: types.DefaultUserServer},
		{User: "5511999999999", Device: 42, Server: types.DefaultUserServer},
	}

	node := BuildTerminateElsewhereStanza(peer, "call-abc", creator, devices)

	if node.Tag != "call" {
		t.Fatalf("outer tag = %q, want call", node.Tag)
	}
	if to, _ := node.Attrs["to"].(types.JID); to.String() != peer.String() {
		t.Errorf("to = %v, want %v", node.Attrs["to"], peer)
	}

	inner, ok := node.Content.([]waBinary.Node)
	if !ok || len(inner) != 1 {
		t.Fatalf("content = %#v, want exactly one child node", node.Content)
	}
	term := inner[0]
	if term.Tag != "terminate" {
		t.Fatalf("inner tag = %q, want terminate", term.Tag)
	}
	if got := term.Attrs["reason"]; got != "accepted_elsewhere" {
		t.Errorf("reason = %v, want accepted_elsewhere", got)
	}
	if got := term.Attrs["call-id"]; got != "call-abc" {
		t.Errorf("call-id = %v, want call-abc", got)
	}
	if got, _ := term.Attrs["call-creator"].(types.JID); got.String() != creator.String() {
		t.Errorf("call-creator = %v, want %v", term.Attrs["call-creator"], creator)
	}

	destWrap, ok := term.Content.([]waBinary.Node)
	if !ok || len(destWrap) != 1 || destWrap[0].Tag != "destination" {
		t.Fatalf("terminate content = %#v, want a single destination node", term.Content)
	}
	tos, ok := destWrap[0].Content.([]waBinary.Node)
	if !ok || len(tos) != len(devices) {
		t.Fatalf("destination has %d children, want %d", len(tos), len(devices))
	}
	for i, to := range tos {
		if to.Tag != "to" {
			t.Errorf("child %d tag = %q, want to", i, to.Tag)
		}
		jid, _ := to.Attrs["jid"].(types.JID)
		if jid.String() != devices[i].String() {
			t.Errorf("child %d jid = %v, want %v", i, to.Attrs["jid"], devices[i])
		}
	}
}

func TestBuildTerminateElsewhereStanzaNoDevices(t *testing.T) {
	peer := types.JID{User: "5511999999999", Server: types.DefaultUserServer}
	node := BuildTerminateElsewhereStanza(peer, "call-abc", peer, nil)

	inner, _ := node.Content.([]waBinary.Node)
	if len(inner) != 1 {
		t.Fatalf("content = %#v, want one child", node.Content)
	}
	destWrap, _ := inner[0].Content.([]waBinary.Node)
	if len(destWrap) != 1 {
		t.Fatalf("terminate content = %#v, want a destination node", inner[0].Content)
	}
	if tos, _ := destWrap[0].Content.([]waBinary.Node); len(tos) != 0 {
		t.Errorf("destination has %d children, want 0", len(tos))
	}
}
