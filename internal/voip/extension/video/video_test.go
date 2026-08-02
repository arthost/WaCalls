package video

import (
	"bytes"
	"testing"

	"wacalls/internal/voip/core"
	"wacalls/internal/voip/engine"
	"wacalls/internal/voip/media"
)

type fakeRelay struct{ connected bool }

func (f *fakeRelay) HasConnection() bool               { return f.connected }
func (f *fakeRelay) Broadcast([]byte)                  {}
func (f *fakeRelay) BufferedAmount() uint64            { return 0 }
func (f *fakeRelay) SetStreamSsrcs([]uint32, []uint32) {}

func annexB(nals ...[]byte) []byte {
	var b []byte
	for _, n := range nals {
		b = append(b, 0, 0, 0, 1)
		b = append(b, n...)
	}
	return b
}

func TestVideoInboundReassembly(t *testing.T) {
	v := New()
	scope := &engine.CallScope{Observer: core.NopObserver{}}
	var registeredPT uint8
	var handler func(*media.RtpPacket)
	scope.OnRTP = func(pt uint8, h func(*media.RtpPacket)) { registeredPT = pt; handler = h }
	if err := v.Attach(scope); err != nil {
		t.Fatal(err)
	}
	if registeredPT != core.PayloadTypeWhatsAppH264 {
		t.Fatalf("want PT %d, got %d", core.PayloadTypeWhatsAppH264, registeredPT)
	}

	idr := append([]byte{0x65}, bytes.Repeat([]byte{0x11}, 30)...)
	want := annexB(idr)
	var got []byte
	var gotKey bool
	v.OnPeerVideo(func(annexb []byte, ts90 uint32, keyframe bool) {
		got = annexb
		gotKey = keyframe
	})

	pkts := media.PacketizeH264(want, 1100)
	for i, p := range pkts {
		handler(&media.RtpPacket{
			Header:  &media.RtpHeader{Marker: i == len(pkts)-1, Timestamp: 12345},
			Payload: p,
		})
	}
	if !gotKey {
		t.Fatal("keyframe flag not propagated")
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("reassembled AU mismatch\nwant %x\ngot  %x", want, got)
	}
}

func TestVideoOutboundForwardsWhenConnected(t *testing.T) {
	v := New()
	relay := &fakeRelay{connected: true}
	var sent []byte
	scope := &engine.CallScope{
		Observer: core.NopObserver{},
		Relay:    relay,
		OnRTP:    func(uint8, func(*media.RtpPacket)) {},
		SendVideoFrame: func(annexb []byte, ts90 uint32) error {
			sent = annexb
			return nil
		},
	}
	if err := v.Attach(scope); err != nil {
		t.Fatal(err)
	}

	frame := annexB([]byte{0x41, 0x01, 0x02})
	v.FeedEncodedVideo(frame, 900)
	if !bytes.Equal(sent, frame) {
		t.Fatalf("outbound frame not forwarded: %x", sent)
	}

	// When the relay has no connection, nothing is forwarded.
	relay.connected = false
	sent = nil
	v.FeedEncodedVideo(frame, 900)
	if sent != nil {
		t.Fatal("frame forwarded while relay disconnected")
	}
}
