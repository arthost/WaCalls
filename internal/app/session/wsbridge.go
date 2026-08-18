package session

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"wacalls/internal/voip/media"

	"github.com/coder/websocket"
)

// WSAudioSubprotocol is the WebSocket subprotocol the browser must request. It
// names the wire format: raw Int16 LE PCM, 16 kHz mono, one binary message per
// frame — the same currency the WebRTC data channel carries.
const WSAudioSubprotocol = "pcm16"

// wsPingInterval keeps the socket warm through idle-connection reapers. Cloudflare
// closes an idle WebSocket after 100 s; reverse proxies are usually stricter.
const wsPingInterval = 20 * time.Second

// wsWriteTimeout bounds a single frame write so a browser that stops reading
// (backgrounded tab, dead network) cannot wedge the peer-audio callback, which
// runs on the call's receive path.
const wsWriteTimeout = 5 * time.Second

// WSBridge is the operator leg over a plain WebSocket instead of WebRTC. It exists
// for deployments where the browser cannot reach the server over UDP — behind a
// reverse proxy that only forwards HTTP (Cloudflare's proxied mode, a corporate
// firewall). WSS rides the same 443 the rest of the API uses, so it always gets
// through; the price is audio only, since we will not push H.264 through a
// non-congestion-controlled socket.
//
// It satisfies browserLeg, so the call plumbing cannot tell which transport an
// operator is on.
type WSBridge struct {
	conn   *websocket.Conn
	ctx    context.Context
	cancel context.CancelFunc
	log    *slog.Logger

	// writeMu serialises Write: coder/websocket allows only one writer in flight
	// and errors out on concurrent use.
	writeMu sync.Mutex

	mu     sync.Mutex
	closed bool

	// OnBrowserPCM is invoked with 16 kHz mono PCM decoded from the browser mic.
	OnBrowserPCM func(pcm []float32)
	// OnTerminal fires when the socket drops without a local Close — the browser
	// went away, so the call should end.
	OnTerminal func()
}

func NewWSBridge(conn *websocket.Conn, log *slog.Logger) *WSBridge {
	ctx, cancel := context.WithCancel(context.Background())
	return &WSBridge{conn: conn, ctx: ctx, cancel: cancel, log: log}
}

// WritePCM sends 16 kHz mono float32 PCM to the browser as Int16 LE.
func (b *WSBridge) WritePCM(pcm []float32) error {
	if len(pcm) == 0 {
		return nil
	}
	b.mu.Lock()
	closed := b.closed
	b.mu.Unlock()
	if closed {
		return nil
	}

	payload := media.PCMFloat32ToInt16LE(pcm)

	b.writeMu.Lock()
	defer b.writeMu.Unlock()
	ctx, cancel := context.WithTimeout(b.ctx, wsWriteTimeout)
	defer cancel()
	return b.conn.Write(ctx, websocket.MessageBinary, payload)
}

// WriteVideo is a no-op: this transport is audio only. Returning nil (rather than
// an error) keeps the peer-video callback quiet instead of logging on every frame
// when the peer sends video to an operator on the WebSocket transport.
func (b *WSBridge) WriteVideo(_ []byte, _ uint32, _ bool) error { return nil }

// Close shuts the socket down and suppresses OnTerminal — a local close is not a
// dropped browser, so it must not end the call a second time.
func (b *WSBridge) Close() {
	b.mu.Lock()
	already := b.closed
	b.closed = true
	b.mu.Unlock()
	b.cancel()
	if !already {
		_ = b.conn.Close(websocket.StatusNormalClosure, "bridge closed")
	}
}

// ReadLoop pumps uplink frames until the socket closes, then fires OnTerminal if
// the close was not local. It blocks, so the HTTP handler that accepted the
// upgrade should call it directly: returning from the handler would close the
// hijacked connection.
func (b *WSBridge) ReadLoop() {
	go b.keepAlive()

	for {
		typ, data, err := b.conn.Read(b.ctx)
		if err != nil {
			break
		}
		if typ != websocket.MessageBinary || len(data) < 2 {
			continue
		}
		if cb := b.OnBrowserPCM; cb != nil {
			cb(media.PCMInt16LEToFloat32(data))
		}
	}

	b.mu.Lock()
	fire := !b.closed
	b.closed = true
	b.mu.Unlock()
	b.cancel()

	if fire && b.OnTerminal != nil {
		b.OnTerminal()
	}
}

// keepAlive pings the browser so proxies with an idle timeout keep the tunnel
// open on a call where nobody is talking.
func (b *WSBridge) keepAlive() {
	t := time.NewTicker(wsPingInterval)
	defer t.Stop()
	for {
		select {
		case <-b.ctx.Done():
			return
		case <-t.C:
			ctx, cancel := context.WithTimeout(b.ctx, wsWriteTimeout)
			err := b.conn.Ping(ctx)
			cancel()
			if err != nil {
				return
			}
		}
	}
}
