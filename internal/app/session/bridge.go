package session

import (
	"encoding/binary"
	"log/slog"
	"sync/atomic"

	"wacalls/internal/voip/media"

	"github.com/pion/webrtc/v4"
)

// pcmChannelLabel is the data channel the browser opens to carry raw 16 kHz mono
// Int16 LE PCM in both directions. The browser side must create it with this label.
const pcmChannelLabel = "pcm"

// videoChannelLabel is the data channel carrying H.264 access units in both
// directions. Each message is framed as: 4-byte big-endian 90 kHz timestamp,
// 1-byte keyframe flag (1=keyframe), then the Annex-B access-unit bytes. The
// browser encodes/decodes with WebCodecs; the Go side never runs a video codec.
const videoChannelLabel = "video"

// videoFrameHeaderLen is the fixed prefix on every video data-channel message:
// uint32 timestamp + uint8 keyframe flag.
const videoFrameHeaderLen = 5

// browserLeg is the operator side of a call, as the call plumbing sees it: a sink
// for peer media plus a teardown. Bridge implements it over WebRTC data channels;
// WSBridge implements it over a plain WebSocket for operators who cannot reach the
// server over UDP.
type browserLeg interface {
	WritePCM(pcm []float32) error
	WriteVideo(annexb []byte, ts90 uint32, keyframe bool) error
	Close()
}

// Bridge is the browser-leg adapter: it carries raw PCM (and, for video calls,
// H.264 access units) between the browser and the CallManager over WebRTC data
// channels. The call core only ever sees []float32 PCM and Annex-B bytes, so it
// stays unaware of the transport.
type Bridge struct {
	pc      *webrtc.PeerConnection
	dc      atomic.Pointer[webrtc.DataChannel]
	videoDC atomic.Pointer[webrtc.DataChannel]
	log     *slog.Logger

	// OnBrowserPCM is invoked with decoded 16 kHz mono PCM captured from the browser mic.
	OnBrowserPCM func(pcm []float32)
	// OnBrowserVideo is invoked with an Annex-B access unit captured and encoded by
	// the browser, along with its 90 kHz timestamp.
	OnBrowserVideo func(annexb []byte, ts90 uint32)
	// OnTerminalICE fires when the peer connection fails or closes.
	OnTerminalICE func()
}

func NewBridge(api *webrtc.API, offerSDP string, log *slog.Logger) (*Bridge, string, error) {
	pc, err := api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return nil, "", err
	}
	br := &Bridge{pc: pc, log: log}

	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		switch dc.Label() {
		case pcmChannelLabel:
			br.dc.Store(dc)
			dc.OnMessage(func(msg webrtc.DataChannelMessage) {
				if cb := br.OnBrowserPCM; cb != nil && len(msg.Data) > 0 {
					cb(media.PCMInt16LEToFloat32(msg.Data))
				}
			})
		case videoChannelLabel:
			br.videoDC.Store(dc)
			dc.OnMessage(func(msg webrtc.DataChannelMessage) {
				cb := br.OnBrowserVideo
				if cb == nil || len(msg.Data) <= videoFrameHeaderLen {
					return
				}
				ts90 := binary.BigEndian.Uint32(msg.Data[:4])
				cb(msg.Data[videoFrameHeaderLen:], ts90)
			})
		}
	})

	pc.OnICEConnectionStateChange(func(s webrtc.ICEConnectionState) {
		log.Debug("browser ice state", "state", s.String())
		if s == webrtc.ICEConnectionStateFailed || s == webrtc.ICEConnectionStateClosed {
			if br.OnTerminalICE != nil {
				br.OnTerminalICE()
			}
		}
	})

	if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offerSDP}); err != nil {
		_ = pc.Close()
		return nil, "", err
	}
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		_ = pc.Close()
		return nil, "", err
	}
	gatherComplete := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(answer); err != nil {
		_ = pc.Close()
		return nil, "", err
	}
	<-gatherComplete

	return br, pc.LocalDescription().SDP, nil
}

// WritePCM sends 16 kHz mono float32 PCM to the browser as Int16 LE over the data
// channel. It is a no-op until the channel is open.
func (b *Bridge) WritePCM(pcm []float32) error {
	dc := b.dc.Load()
	if dc == nil || len(pcm) == 0 {
		return nil
	}
	return dc.Send(media.PCMFloat32ToInt16LE(pcm))
}

// WriteVideo sends a peer H.264 access unit to the browser over the video data
// channel, framed with its 90 kHz timestamp and keyframe flag. No-op until the
// channel is open.
func (b *Bridge) WriteVideo(annexb []byte, ts90 uint32, keyframe bool) error {
	dc := b.videoDC.Load()
	if dc == nil || len(annexb) == 0 {
		return nil
	}
	msg := make([]byte, videoFrameHeaderLen+len(annexb))
	binary.BigEndian.PutUint32(msg[:4], ts90)
	if keyframe {
		msg[4] = 1
	}
	copy(msg[videoFrameHeaderLen:], annexb)
	return dc.Send(msg)
}

func (b *Bridge) Close() {
	if b.pc != nil {
		_ = b.pc.Close()
	}
}
