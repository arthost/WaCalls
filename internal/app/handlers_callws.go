package app

import (
	"net/http"
	"strings"
	"time"

	"wacalls/internal/app/session"

	"github.com/coder/websocket"
)

// wsReadLimit caps one uplink frame at 16 KiB — 8192 samples, half a second of
// 16 kHz mono audio. Real frames are ~1 KiB; anything near this is a client bug or
// an attempt to make us allocate.
const wsReadLimit = 16 << 10

// handleCallWS upgrades to the WebSocket audio transport and serves the operator
// leg of a call over it, for browsers that cannot reach the media port over UDP
// (reverse proxy in front of the service, firewall dropping UDP). The socket
// carries the same Int16 LE 16 kHz mono PCM the WebRTC data channel does, so
// nothing downstream of the bridge changes.
//
// Auth already happened in withAuth. A cross-origin handshake cannot carry our
// SameSite=Strict session cookie, so an operator on another origin must pass
// ?apiKey= — requestToken already accepts it.
func (s *Server) handleCallWS(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionByID(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	callID := r.PathValue("id")
	if !sess.HasCall(callID) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such call"})
		return
	}

	// The server's ReadTimeout/IdleTimeout would fire mid-call on the hijacked
	// connection; a call outlives both by design.
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Time{})
	_ = rc.SetWriteDeadline(time.Time{})

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols:   []string{session.WSAudioSubprotocol},
		OriginPatterns: s.wsOriginPatterns(),
	})
	if err != nil {
		s.log.Warn("call ws upgrade failed", "session", r.PathValue("sid"), "call", callID, "err", err)
		return
	}
	conn.SetReadLimit(wsReadLimit)

	s.log.Info("call ws attached", "session", r.PathValue("sid"), "call", callID)
	if err := sess.AttachBrowserWS(callID, conn); err != nil {
		_ = conn.Close(websocket.StatusInternalError, "attach failed")
		s.log.Warn("call ws attach failed", "call", callID, "err", err)
		return
	}
	s.log.Info("call ws detached", "session", r.PathValue("sid"), "call", callID)
}

// wsOriginPatterns translates the configured CORS origins into the host patterns
// coder/websocket matches Origin against. It deliberately mirrors withCORS,
// including the "nothing configured means allow any origin" default: an operator
// whose origin passes CORS but silently fails the WebSocket handshake is very hard
// to debug, and cross-site request forgery on this route is already blocked by the
// session cookie's SameSite=Strict.
func (s *Server) wsOriginPatterns() []string {
	if len(s.allowedOrigins) == 0 {
		return []string{"*"}
	}
	out := make([]string, 0, len(s.allowedOrigins))
	for origin := range s.allowedOrigins {
		if origin == "*" {
			return []string{"*"}
		}
		host := origin
		if i := strings.Index(host, "://"); i >= 0 {
			host = host[i+3:]
		}
		host = strings.TrimSuffix(host, "/")
		if host != "" {
			out = append(out, host)
		}
	}
	return out
}
