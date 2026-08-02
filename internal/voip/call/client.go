package call

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"wacalls/internal/voip/core"
	"wacalls/internal/voip/engine"
	"wacalls/internal/voip/signaling"
	"wacalls/internal/voip/wanode"

	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/types"
)

type Client struct {
	sock           core.VoipSocket
	log            *slog.Logger
	makeExtensions func() []engine.Extension
	newObserver    func(callID string) core.CallObserver
	onCall         func(callID string, cm *CallManager)
	maxCalls       int
	mu             sync.Mutex
	calls          map[string]*CallManager
}

func NewClient(sock core.VoipSocket, log *slog.Logger, makeExtensions func() []engine.Extension, maxCalls int, onCall func(callID string, cm *CallManager), newObserver func(callID string) core.CallObserver) *Client {
	if newObserver == nil {
		newObserver = func(string) core.CallObserver { return core.NopObserver{} }
	}
	return &Client{sock: sock, log: log, makeExtensions: makeExtensions, newObserver: newObserver, onCall: onCall, maxCalls: maxCalls, calls: map[string]*CallManager{}}
}

func (c *Client) createCall(callID string) *CallManager {
	cm := NewCallManager(c.sock, c.log, c.makeExtensions()...)
	cm.observer = core.MultiObserver(c.newObserver(callID), markTap{cm: cm, callID: callID, start: time.Now()})
	c.onCall(callID, cm)
	c.mu.Lock()
	c.calls[callID] = cm
	c.mu.Unlock()
	return cm
}

func (c *Client) get(callID string) (*CallManager, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cm, ok := c.calls[callID]
	return cm, ok
}

func (c *Client) Get(callID string) (*CallManager, bool) {
	return c.get(callID)
}

func (c *Client) Remove(callID string) {
	c.mu.Lock()
	delete(c.calls, callID)
	c.mu.Unlock()
}

func (c *Client) Count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.calls)
}

func (c *Client) Drain() []*CallManager {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]*CallManager, 0, len(c.calls))
	for _, cm := range c.calls {
		out = append(out, cm)
	}
	c.calls = map[string]*CallManager{}
	return out
}

func (c *Client) StartCall(ctx context.Context, peer types.JID, video bool) (string, error) {
	callID := signaling.GenerateCallID()
	cm := c.createCall(callID)
	if err := cm.StartCall(ctx, callID, peer, video); err != nil {
		c.Remove(callID)
		return "", err
	}
	return callID, nil
}

func (c *Client) HandleOffer(ctx context.Context, node *waBinary.Node, peer types.JID) {
	info := signaling.ExtractNodeInfo(node)
	if info == nil || info.CallID == "" {
		return
	}
	if _, ok := c.get(info.CallID); ok {
		c.log.Info("duplicate offer ignored", "call_id", info.CallID)
		return
	}
	if c.maxCalls > 0 && c.Count() >= c.maxCalls {
		c.reject(ctx, node, peer, "session at capacity")
		return
	}
	callKey, err := signaling.DecryptCallKeyInNode(ctx, c.sock, info.InnerNode, peer)
	if err != nil {
		c.log.Error("offer call key undecryptable; rejecting call",
			"call_id", info.CallID, "peer", peer.String(), "err", err,
			"hint", "signal session desync: exchange a message with the contact or re-pair")
		c.reject(ctx, node, peer, "undecryptable call key")
		return
	}
	cm := c.createCall(info.CallID)
	cm.HandleCallOffer(ctx, node, peer, callKey)
}

func (c *Client) reject(ctx context.Context, node *waBinary.Node, peer types.JID, why string) {
	info := signaling.ExtractNodeInfo(node)
	if info == nil {
		return
	}
	creator := wanode.AttrString(info.InnerNode.Attrs, "call-creator")
	if creator == "" {
		creator = peer.String()
	}
	reject := signaling.BuildRejectStanza(peer, info.CallID, wanode.MustJID(creator))
	_ = c.sock.SendNode(ctx, reject)
	c.log.Info("inbound call rejected: "+why, "call_id", info.CallID)
}

func (c *Client) HandleAccept(ctx context.Context, node *waBinary.Node, peer types.JID) {
	info := signaling.ExtractNodeInfo(node)
	if info == nil {
		return
	}
	if cm, ok := c.get(info.CallID); ok {
		cm.HandleCallAccept(ctx, node, peer)
	}
}

func (c *Client) HandleTransport(ctx context.Context, node *waBinary.Node, peer types.JID) {
	info := signaling.ExtractNodeInfo(node)
	if info == nil {
		return
	}
	if cm, ok := c.get(info.CallID); ok {
		cm.HandleCallTransport(ctx, node, peer)
	}
}

func (c *Client) HandleRelayLatency(ctx context.Context, node *waBinary.Node, peer types.JID) {
	info := signaling.ExtractNodeInfo(node)
	if info == nil {
		return
	}
	if cm, ok := c.get(info.CallID); ok {
		cm.HandleCallRelayLatency(ctx, node, peer)
	}
}

func (c *Client) HandleTerminate(node *waBinary.Node) {
	info := signaling.ExtractNodeInfo(node)
	if info == nil {
		return
	}
	if cm, ok := c.get(info.CallID); ok {
		cm.HandleCallTerminate(node)
	}
}

// HandleVideoState routes an inbound mid-call <call><video state=N> stanza to
// its CallManager. The call-id lives on the <video> child (not the <call>
// attrs), so we resolve it via ParseVideoState rather than ExtractNodeInfo.
func (c *Client) HandleVideoState(ctx context.Context, node *waBinary.Node, peer types.JID) {
	parsed := signaling.ParseVideoState(node)
	if !parsed.Found || parsed.CallID == "" {
		return
	}
	if cm, ok := c.get(parsed.CallID); ok {
		cm.HandleVideoState(ctx, node)
	}
}

// EnableVideo turns on the local camera stream mid-call (audio→video upgrade)
// for the given call, delegating to the CallManager.
func (c *Client) EnableVideo(ctx context.Context, callID string) error {
	if cm, ok := c.get(callID); ok {
		return cm.EnableLocalVideo(ctx)
	}
	return &CallError{"no call with id " + callID}
}

func (c *Client) HoldCall(ctx context.Context, callID string, holdMusic []float32) error {
	if cm, ok := c.get(callID); ok {
		return cm.Hold(holdMusic)
	}
	return &CallError{"no call with id " + callID}
}

func (c *Client) ResumeCall(ctx context.Context, callID string) error {
	if cm, ok := c.get(callID); ok {
		return cm.Resume()
	}
	return &CallError{"no call with id " + callID}
}

func (c *Client) AcceptCall(ctx context.Context, callID string) error {
	if cm, ok := c.get(callID); ok {
		return cm.AcceptCall(ctx, callID)
	}
	return &CallError{"no call with id " + callID}
}

func (c *Client) RejectCall(ctx context.Context, callID string, reason core.EndCallReason) error {
	if cm, ok := c.get(callID); ok {
		return cm.RejectCall(ctx, callID, reason)
	}
	return &CallError{"no call with id " + callID}
}

func (c *Client) EndCall(ctx context.Context, callID string, reason core.EndCallReason) error {
	if cm, ok := c.get(callID); ok {
		return cm.EndCall(ctx, reason)
	}
	return nil
}
