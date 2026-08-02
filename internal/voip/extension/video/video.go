package video

import (
	"sync"

	"wacalls/internal/voip/core"
	"wacalls/internal/voip/engine"
	"wacalls/internal/voip/media"
)

const videoBufferBytes = 64 * 1024

// Video is the H.264 leg of a call. Unlike the audio extension it runs no codec:
// the browser's WebCodecs does the encoding/decoding. Outbound frames captured
// by the browser are forwarded to the RTP packetizer via the scope; inbound RTP
// (PT 97) is reassembled into Annex-B access units and handed back toward the
// browser. It stays dormant on audio-only calls — nothing feeds it, so it
// neither sends nor allocates a send loop.
type Video struct {
	mu          sync.Mutex
	scope       *engine.CallScope
	depkt       media.H264Depacketizer
	onPeerVideo func(annexb []byte, ts90 uint32, keyframe bool)
	detached    bool
}

func New() *Video { return &Video{} }

func (v *Video) Name() string { return "video" }

func (v *Video) Attach(scope *engine.CallScope) error {
	v.mu.Lock()
	v.scope = scope
	v.depkt = media.H264Depacketizer{}
	scope.Observer.AddMem(videoBufferBytes)
	v.mu.Unlock()
	scope.OnRTP(core.PayloadTypeWhatsAppH264, v.handleInbound)
	return nil
}

func (v *Video) Detach() {
	v.mu.Lock()
	if v.detached {
		v.mu.Unlock()
		return
	}
	v.detached = true
	scope := v.scope
	v.mu.Unlock()
	if scope != nil {
		scope.Observer.ReleaseMem(videoBufferBytes)
	}
}

// FeedEncodedVideo forwards one browser-encoded Annex-B access unit (with its
// 90 kHz timestamp) to the RTP packetizer. It is a no-op until the call's media
// path is up.
func (v *Video) FeedEncodedVideo(annexb []byte, ts90 uint32) {
	if len(annexb) == 0 {
		return
	}
	v.mu.Lock()
	scope := v.scope
	v.mu.Unlock()
	if scope == nil || scope.SendVideoFrame == nil {
		return
	}
	if scope.Relay == nil || !scope.Relay.HasConnection() {
		return
	}
	_ = scope.SendVideoFrame(annexb, ts90)
}

func (v *Video) OnPeerVideo(handler func(annexb []byte, ts90 uint32, keyframe bool)) {
	v.mu.Lock()
	v.onPeerVideo = handler
	v.mu.Unlock()
}

func (v *Video) handleInbound(pkt *media.RtpPacket) {
	v.mu.Lock()
	au, keyframe, complete := v.depkt.Push(pkt.Payload, pkt.Header.Marker)
	cb := v.onPeerVideo
	v.mu.Unlock()
	if complete && cb != nil {
		cb(au, pkt.Header.Timestamp, keyframe)
	}
}

var _ core.VideoSink = (*Video)(nil)
var _ engine.Extension = (*Video)(nil)
