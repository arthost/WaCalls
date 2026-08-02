package app

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"wacalls/internal/app/events"
	"wacalls/internal/app/session"
	"wacalls/internal/voip/call"
	"wacalls/internal/voip/core"
)

func (s *Server) handleStartCall(w http.ResponseWriter, r *http.Request) {
	if sess := s.sessionByID(w, r.PathValue("sid")); sess != nil {
		s.doStartCall(sess, w, r)
	}
}

func (s *Server) handleWebRTC(w http.ResponseWriter, r *http.Request) {
	if sess := s.sessionByID(w, r.PathValue("sid")); sess != nil {
		s.doWebRTC(sess, w, r)
	}
}

func (s *Server) handleAccept(w http.ResponseWriter, r *http.Request) {
	if sess := s.sessionByID(w, r.PathValue("sid")); sess != nil {
		s.doAccept(sess, w, r)
	}
}

func (s *Server) handleReject(w http.ResponseWriter, r *http.Request) {
	if sess := s.sessionByID(w, r.PathValue("sid")); sess != nil {
		s.doReject(sess, w, r)
	}
}

func (s *Server) handleEndCall(w http.ResponseWriter, r *http.Request) {
	if sess := s.sessionByID(w, r.PathValue("sid")); sess != nil {
		s.doEndCall(sess, w, r)
	}
}

func (s *Server) handleEnableVideo(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionByID(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	id := r.PathValue("id")
	if !sess.HasCall(id) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such call"})
		return
	}
	if err := sess.EnableVideo(r.Context(), id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleHold(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionByID(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	id := r.PathValue("id")
	if !sess.HasCall(id) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such call"})
		return
	}
	var body struct {
		Action string `json:"action"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	var err error
	if body.Action == "resume" {
		err = sess.ResumeCall(r.Context(), id)
	} else {
		err = sess.HoldCall(r.Context(), id)
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleTransfer(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionByID(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	id := r.PathValue("id")
	if !sess.HasCall(id) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such call"})
		return
	}
	var body struct {
		To string `json:"to"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.To == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "to operator required"})
		return
	}
	from := clientID(r)
	if err := sess.TransferCall(r.Context(), id, body.To, from); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleRecord(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionByID(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	id := r.PathValue("id")
	if !sess.HasCall(id) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such call"})
		return
	}
	var body struct {
		Action string `json:"action"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	var err error
	if body.Action == "stop" {
		err = sess.StopRecording(id)
	} else {
		err = sess.StartRecording(id)
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleHoldMusicGet(w http.ResponseWriter, r *http.Request) {
	diskPath := filepath.Join(s.dataDir, "hold-music.wav")
	st, err := os.Stat(diskPath)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"exists": false,
			"name":   "Default Chime (Embedded)",
			"size":   0,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"exists": true,
		"name":   st.Name(),
		"size":   st.Size(),
	})
}

func (s *Server) handleHoldMusicUpload(w http.ResponseWriter, r *http.Request) {
	file, _, err := r.FormFile("file")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "file field required"})
		return
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil || len(data) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "failed to read upload file"})
		return
	}

	if err := os.MkdirAll(s.dataDir, 0755); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	destPath := filepath.Join(s.dataDir, "hold-music.wav")
	tempPath := destPath + ".tmp"

	if err := os.WriteFile(tempPath, data, 0644); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	if err := os.Rename(tempPath, destPath); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
		"size":   len(data),
	})
}

func (s *Server) doStartCall(sess *session.Session, w http.ResponseWriter, r *http.Request) {
	if !sess.IsPaired() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "not paired"})
		return
	}
	var body struct {
		Phone   string `json:"phone"`
		IsVideo bool   `json:"is_video"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Phone) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "phone required"})
		return
	}
	phone := normalizePhone(body.Phone)
	if phone == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid phone"})
		return
	}
	owner := clientID(r)
	if other := s.broker.OwnerActiveCall(owner); other != "" {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "operator already on a call"})
		return
	}
	st, err := sess.StartCall(r.Context(), phone, body.IsVideo)
	if errors.Is(err, session.ErrTooManyCalls) {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "max concurrent calls"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	s.broker.UpsertCall(events.CallRecord{
		SessionID: sess.ID(), CallID: st.CallID, Owner: events.OwnerRef(owner), Direction: "outbound",
		Peer: st.Peer, PeerName: st.PeerName, PeerPhotoURL: st.PeerPhotoURL,
		StartedAt: time.Now().UnixMilli(), Status: events.StatusRinging,
	})
	sess.FetchCallPhoto(st.CallID, st.Peer)
	writeJSON(w, http.StatusOK, map[string]any{"call": map[string]string{"callId": st.CallID}})
}

func (s *Server) doWebRTC(sess *session.Session, w http.ResponseWriter, r *http.Request) {
	callID := r.PathValue("id")
	if !sess.HasCall(callID) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such call"})
		return
	}
	var body struct {
		SDPOffer string `json:"sdp_offer"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.SDPOffer == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "sdp_offer required"})
		return
	}
	answer, err := sess.AttachBrowser(callID, body.SDPOffer)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"sdp_answer": answer})
}

func (s *Server) doAccept(sess *session.Session, w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !sess.HasCall(id) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such call"})
		return
	}
	owner := clientID(r)
	if other := s.broker.OwnerActiveCall(owner); other != "" && other != id {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "operator already on a call"})
		return
	}
	if !s.broker.SetOwner(id, owner) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "claimed by another client"})
		return
	}
	s.broker.EmitIncomingClaimed(sess.ID(), id, owner)
	if err := sess.AcceptCall(r.Context(), id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"call": map[string]string{"callId": id}})
}

func (s *Server) handleCallList(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionByID(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"calls": s.broker.SessionCalls(sess.ID())})
}

func (s *Server) handleCallGet(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionByID(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	rec, ok := s.broker.GetCall(r.PathValue("id"))
	if !ok || rec.SessionID != sess.ID() {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such call"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"call": rec})
}

func (s *Server) doReject(sess *session.Session, w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var invalid *call.InvalidTransition
	if err := sess.RejectCall(r.Context(), id); errors.As(err, &invalid) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	s.broker.EndCall(id, string(core.EndCallReasonDeclined))
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) doEndCall(sess *session.Session, w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	_ = sess.EndCall(r.Context(), id)
	s.broker.EndCall(id, string(core.EndCallReasonUserEnded))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleStartGroupCall(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionByID(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	var body struct {
		Phones   []string `json:"phones"`
		IsVideo  bool     `json:"is_video"`
		GroupJID string   `json:"group_jid"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Phones) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "at least one phone is required"})
		return
	}
	var cleanPhones []string
	for _, p := range body.Phones {
		if cp := normalizePhone(p); cp != "" {
			cleanPhones = append(cleanPhones, cp)
		}
	}
	if len(cleanPhones) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no valid phone numbers"})
		return
	}

	st, err := sess.StartCall(r.Context(), cleanPhones[0], body.IsVideo)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	owner := clientID(r)
	s.broker.UpsertCall(events.CallRecord{
		SessionID: sess.ID(), CallID: st.CallID, Owner: events.OwnerRef(owner), Direction: "outbound",
		Peer: strings.Join(cleanPhones, ","), PeerName: body.GroupJID,
		StartedAt: time.Now().UnixMilli(), Status: events.StatusRinging,
	})

	s.broker.EmitGroupCall(sess.ID(), st.CallID, cleanPhones, body.IsVideo, body.GroupJID)
	writeJSON(w, http.StatusOK, map[string]any{
		"call": map[string]any{
			"callId":   st.CallID,
			"phones":   cleanPhones,
			"isVideo":  body.IsVideo,
			"groupJid": body.GroupJID,
		},
	})
}

func (s *Server) handleCallControl(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionByID(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	callID := r.PathValue("id")
	var body struct {
		Action      string         `json:"action"`
		Participant string         `json:"participant"`
		Emoji       string         `json:"emoji"`
		Extra       map[string]any `json:"extra"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Action == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "action is required"})
		return
	}
	s.broker.EmitCallControl(sess.ID(), callID, body.Action, body.Participant, body.Emoji, body.Extra)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleCallLobby(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionByID(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	callID := r.PathValue("id")
	var body struct {
		Participant string `json:"participant"`
		Action      string `json:"action"` // "admit" | "reject"
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Participant == "" || body.Action == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "participant and action required"})
		return
	}
	s.broker.EmitCallLobby(sess.ID(), callID, body.Participant, body.Action)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleCallParticipants(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionByID(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	callID := r.PathValue("id")
	var body struct {
		Phone      string   `json:"phone"`
		Action     string   `json:"action"` // "add" | "remove" | "rering"
		PhonesList []string `json:"phones"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Action == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "action required"})
		return
	}
	cleanPhone := normalizePhone(body.Phone)
	s.broker.EmitCallParticipants(sess.ID(), callID, cleanPhone, body.Action, body.PhonesList)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleCreateCallLink(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionByID(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	var body struct {
		Title       string `json:"title"`
		ScheduledAt int64  `json:"scheduled_at"`
		IsVideo     bool   `json:"is_video"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.Title == "" {
		body.Title = "Reunião DUO CRM"
	}
	token := time.Now().Format("20060102150405") + "-" + sess.ID()[:6]
	linkURL := "/call-room/" + token

	writeJSON(w, http.StatusOK, map[string]any{
		"token":        token,
		"url":          linkURL,
		"title":        body.Title,
		"scheduled_at": body.ScheduledAt,
		"is_video":     body.IsVideo,
	})
}

func (s *Server) handleGetCallLink(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionByID(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	token := r.PathValue("token")
	writeJSON(w, http.StatusOK, map[string]any{
		"token": token,
		"valid": true,
		"title": "Reunião DUO CRM",
	})
}

func normalizePhone(p string) string {
	p = strings.TrimSpace(p)
	p = strings.TrimPrefix(p, "+")
	var b strings.Builder
	for _, c := range p {
		if c >= '0' && c <= '9' {
			b.WriteRune(c)
		}
	}
	return b.String()
}
