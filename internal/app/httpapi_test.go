package app

import (
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"wacalls/internal/app/events"
	"wacalls/internal/app/session"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
)

func startCallSession(jid *types.JID) *session.Session {
	mgr := session.NewManager(session.Deps{Log: slog.Default()})
	return mgr.NewSession("s1", "", &whatsmeow.Client{Store: &store.Device{ID: jid}})
}

func TestStartCallRejectsPhoneWithoutDigits(t *testing.T) {
	jid := types.NewJID("5511888880000", types.DefaultUserServer)
	sess := startCallSession(&jid)
	s := &Server{}

	rec := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/sessions/s1/calls", strings.NewReader(`{"phone":"abc"}`))
	s.doStartCall(sess, rec, r)

	if rec.Code != 400 {
		t.Fatalf("expected 400 for phone without digits, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "invalid phone") {
		t.Fatalf("expected invalid phone error, got %q", rec.Body.String())
	}
}

func TestStartCallToleratesLegacyFields(t *testing.T) {
	jid := types.NewJID("5511888880000", types.DefaultUserServer)
	sess := startCallSession(&jid)
	s := &Server{}

	rec := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/sessions/s1/calls",
		strings.NewReader(`{"phone":"abc","duration_ms":300000,"record":true}`))
	s.doStartCall(sess, rec, r)

	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "invalid phone") {
		t.Fatalf("legacy fields must decode fine and reach phone validation, got %d %q", rec.Code, rec.Body.String())
	}
}

func TestNormalizePhone(t *testing.T) {
	cases := map[string]string{
		"+55 (11) 99999-0000": "5511999990000",
		"abc":                 "",
		" +() -":              "",
		"5511999990000":       "5511999990000",
	}
	for in, want := range cases {
		if got := normalizePhone(in); got != want {
			t.Errorf("normalizePhone(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsSamePhoneNumber(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		{"5511999990000", "5511999990000", true},
		{"+55 11 99999-0000", "5511999990000", true},
		{"5511999990000", "551199990000", true}, // 9th digit variation BR
		{"551199990000", "5511999990000", true}, // 9th digit variation BR
		{"5511999990000", "5521999990000", false}, // different DDD
		{"5511999990000", "5511888880000", false}, // different number
		{"", "5511999990000", false},
	}
	for _, tc := range tests {
		got := isSamePhoneNumber(tc.a, tc.b)
		if got != tc.want {
			t.Errorf("isSamePhoneNumber(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestStartCallRejectsSelfCall(t *testing.T) {
	jid := types.NewJID("5511999990000", types.DefaultUserServer)
	sess := startCallSession(&jid)
	s := &Server{}

	rec := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/sessions/s1/calls", strings.NewReader(`{"phone":"5511999990000"}`))
	s.doStartCall(sess, rec, r)

	if rec.Code != 400 {
		t.Fatalf("expected 400 for self-call, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "não pode ligar para si mesmo") {
		t.Fatalf("expected self-call error message, got %q", rec.Body.String())
	}
}

func TestHandleSystemMetrics(t *testing.T) {
	mgr := session.NewManager(session.Deps{Ctx: t.Context(), Log: slog.Default()})
	s := &Server{
		startTime: time.Now(),
		sessions:  mgr,
		broker:    events.NewBroker(nil, slog.Default()),
	}

	rec := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/system/metrics", nil)
	s.handleSystemMetrics(rec, r)

	if rec.Code != 200 {
		t.Fatalf("expected 200 for system metrics, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "uptimeSeconds") || !strings.Contains(body, "process") || !strings.Contains(body, "host") {
		t.Fatalf("unexpected system metrics body: %s", body)
	}
}

