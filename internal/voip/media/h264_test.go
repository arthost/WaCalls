package media

import (
	"bytes"
	"testing"
)

func annexB(nals ...[]byte) []byte {
	var b []byte
	for _, n := range nals {
		b = append(b, 0, 0, 0, 1)
		b = append(b, n...)
	}
	return b
}

func TestPacketizeSingleNAL(t *testing.T) {
	nal := []byte{0x41, 0x9a, 0x01, 0x02, 0x03} // non-IDR slice (type 1)
	pkts := PacketizeH264(annexB(nal), 1100)
	if len(pkts) != 1 {
		t.Fatalf("want 1 packet, got %d", len(pkts))
	}
	if !bytes.Equal(pkts[0], nal) {
		t.Fatalf("single-NAL payload mismatch: %x", pkts[0])
	}
}

func TestPacketizeFUARoundTrip(t *testing.T) {
	// A large IDR slice (type 5) that must fragment into several FU-A packets.
	body := make([]byte, 5000)
	for i := range body {
		body[i] = byte(i)
	}
	nal := append([]byte{0x65}, body...) // NAL header type 5, nri 3
	pkts := PacketizeH264(annexB(nal), 1100)
	if len(pkts) < 2 {
		t.Fatalf("want fragmentation, got %d packets", len(pkts))
	}

	var d H264Depacketizer
	var au []byte
	var key, complete bool
	for i, p := range pkts {
		marker := i == len(pkts)-1
		au, key, complete = d.Push(p, marker)
	}
	if !complete {
		t.Fatal("access unit never completed")
	}
	if !key {
		t.Fatal("IDR access unit not flagged as keyframe")
	}
	if !bytes.Equal(au, annexB(nal)) {
		t.Fatalf("reassembled AU mismatch\nwant %x\ngot  %x", annexB(nal), au)
	}
}

func TestDepacketizeMultiNALAccessUnit(t *testing.T) {
	sps := []byte{0x67, 0x42, 0x00, 0x1e}
	pps := []byte{0x68, 0xce, 0x3c, 0x80}
	idr := append([]byte{0x65}, bytes.Repeat([]byte{0xAB}, 20)...)
	in := annexB(sps, pps, idr)

	pkts := PacketizeH264(in, 1100)
	var d H264Depacketizer
	var au []byte
	var key, complete bool
	for i, p := range pkts {
		au, key, complete = d.Push(p, i == len(pkts)-1)
	}
	if !complete || !key {
		t.Fatalf("expected complete keyframe AU, complete=%v key=%v", complete, key)
	}
	if !bytes.Equal(au, in) {
		t.Fatalf("multi-NAL AU mismatch\nwant %x\ngot  %x", in, au)
	}
}

func TestH264SessionPayloadType(t *testing.T) {
	s := NewWhatsAppH264Session(0x1234)
	pkt := s.CreatePacketAt([]byte{0x41, 0x01}, 90000, true)
	if pkt.Header.PayloadType != 97 {
		t.Fatalf("want PT 97, got %d", pkt.Header.PayloadType)
	}
	if pkt.Header.Timestamp != 90000 {
		t.Fatalf("timestamp not honored: %d", pkt.Header.Timestamp)
	}
	if !pkt.Header.Marker {
		t.Fatal("marker not set")
	}
	next := s.CreatePacketAt([]byte{0x41, 0x02}, 90000, false)
	if next.Header.SequenceNumber != pkt.Header.SequenceNumber+1 {
		t.Fatal("sequence number did not advance")
	}
}
