package app

import (
	"encoding/json"
	"net/http"
	"strings"
)

func (s *Server) handleSessionList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"sessions": s.sessions.Infos()})
}

func (s *Server) handleSessionCreate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	name := strings.TrimSpace(body.Name)
	if name == "" {
		name = "Session"
	}
	customID := strings.TrimSpace(body.ID)
	id, err := s.sessions.Create(name, customID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id})
}

func (s *Server) handleSessionDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.sessions.Delete(r.Context(), r.PathValue("sid")); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleSessionLogout(w http.ResponseWriter, r *http.Request) {
	if err := s.sessions.Logout(r.Context(), r.PathValue("sid")); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleSessionPair(w http.ResponseWriter, r *http.Request) {
	if err := s.sessions.Pair(r.PathValue("sid")); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleSessionPairCode links a session with the 8-digit code WhatsApp accepts under
// "Link with phone number instead". It is the only route when the phone cannot see the
// browser's screen to scan a QR — the common case for a remote operator being onboarded
// over a call.
func (s *Server) handleSessionPairCode(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Phone string `json:"phone"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "phone required"})
		return
	}
	// WhatsApp needs the full international number; the shortest real one is ~8 digits
	// (country code included), and a local-format number silently fails to pair.
	phone := normalizePhone(body.Phone)
	if len(phone) < 8 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "phone must be in international format (country code first), digits only"})
		return
	}
	code, err := s.sessions.PairPhone(r.PathValue("sid"), phone)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"code": code})
}

func (s *Server) handleSessionQR(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionByID(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	info := sess.Info()
	writeJSON(w, http.StatusOK, map[string]string{"qr": info.QR})
}

func (s *Server) handleSessionStatus(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionByID(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	writeJSON(w, http.StatusOK, sess.Info())
}
