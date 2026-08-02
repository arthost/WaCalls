package engine

import (
	"log/slog"

	"wacalls/internal/voip/core"
	"wacalls/internal/voip/media"
)

type CallScope struct {
	Log             *slog.Logger
	CallID          string
	OwnDeviceJID    string
	PeerDeviceJID   string
	Relay           core.Relay
	SendAudioFrame  func(encoded []byte, frameSamples int) error
	SendVideoFrame  func(annexb []byte, ts90 uint32) error
	OnRTP           func(pt uint8, handler func(pkt *media.RtpPacket))
	DeclareSelfSSRC func(ssrc uint32)
	Observer        core.CallObserver
	FrameOverride   func(out []float32) bool
}

type Extension interface {
	Name() string
	Attach(scope *CallScope) error
	Detach()
}

func Capability[T any](exts []Extension) (T, bool) {
	for _, e := range exts {
		if t, ok := e.(T); ok {
			return t, true
		}
	}
	var zero T
	return zero, false
}
