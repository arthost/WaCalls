package media

// RFC 6184 H.264 RTP payload format: the CRM does no video encoding itself —
// the browser's WebCodecs encoder/decoder produce and consume Annex-B H.264,
// and this file is the wire glue between those Annex-B access units and the
// SRTP relay path. Outbound: PacketizeH264 fragments one access unit into RTP
// payloads (single-NAL or FU-A). Inbound: H264Depacketizer reassembles the
// payloads (single-NAL, STAP-A, FU-A) back into an Annex-B access unit.

const (
	naluTypeSTAPA = 24
	naluTypeFUA   = 28

	// h264MaxPayload bounds each RTP payload (excluding the RTP header) so the
	// resulting SRTP packet stays comfortably under the relay/SCTP MTU after the
	// auth tag and transport framing are added.
	h264MaxPayload = 1100
)

// AnnexBNALUs splits an Annex-B bytestream into its NAL units (without the
// start codes). A buffer with no start code is returned as a single NAL.
func AnnexBNALUs(data []byte) [][]byte {
	n := len(data)
	if n == 0 {
		return nil
	}
	i, start := 0, -1
	// Locate the first start code.
	for i+3 <= n {
		if data[i] == 0 && data[i+1] == 0 && data[i+2] == 1 {
			start = i + 3
			i += 3
			break
		}
		if i+4 <= n && data[i] == 0 && data[i+1] == 0 && data[i+2] == 0 && data[i+3] == 1 {
			start = i + 4
			i += 4
			break
		}
		i++
	}
	if start < 0 {
		return [][]byte{data}
	}
	var nals [][]byte
	for i+3 <= n {
		if data[i] == 0 && data[i+1] == 0 && data[i+2] == 1 {
			nals = appendNonEmpty(nals, data[start:i])
			start = i + 3
			i += 3
			continue
		}
		if i+4 <= n && data[i] == 0 && data[i+1] == 0 && data[i+2] == 0 && data[i+3] == 1 {
			nals = appendNonEmpty(nals, data[start:i])
			start = i + 4
			i += 4
			continue
		}
		i++
	}
	nals = appendNonEmpty(nals, data[start:n])
	return nals
}

func appendNonEmpty(nals [][]byte, nal []byte) [][]byte {
	if len(nal) == 0 {
		return nals
	}
	return append(nals, nal)
}

// PacketizeH264 fragments one Annex-B access unit into RTP payloads. Each NAL
// unit small enough for maxPayload is emitted as a single-NAL packet; larger
// ones are split into FU-A fragments. The caller sets the RTP marker bit on the
// last returned payload (end of the access unit). maxPayload <= 2 falls back to
// the default.
func PacketizeH264(annexb []byte, maxPayload int) [][]byte {
	if maxPayload <= 2 {
		maxPayload = h264MaxPayload
	}
	var out [][]byte
	for _, nal := range AnnexBNALUs(annexb) {
		if len(nal) <= maxPayload {
			out = append(out, cloneBytes(nal))
			continue
		}
		nalHeader := nal[0]
		fuIndicator := (nalHeader & 0xE0) | naluTypeFUA
		fuType := nalHeader & 0x1F
		body := nal[1:]
		chunk := maxPayload - 2
		for i := 0; i < len(body); i += chunk {
			end := i + chunk
			if end > len(body) {
				end = len(body)
			}
			fuHeader := fuType
			if i == 0 {
				fuHeader |= 0x80 // Start bit
			}
			if end == len(body) {
				fuHeader |= 0x40 // End bit
			}
			pkt := make([]byte, 2+(end-i))
			pkt[0] = fuIndicator
			pkt[1] = fuHeader
			copy(pkt[2:], body[i:end])
			out = append(out, pkt)
		}
	}
	return out
}

func cloneBytes(b []byte) []byte { return append([]byte(nil), b...) }

// H264Depacketizer reassembles a single H.264 stream (one SSRC) from RTP
// payloads into Annex-B access units. It is not safe for concurrent use.
type H264Depacketizer struct {
	fu     []byte // FU-A fragment accumulation for the NAL in flight
	fuHdr  bool   // whether an FU-A start was seen for the current fragment
	au     []byte // access-unit accumulation (Annex-B, with start codes)
	hasKey bool   // whether the current access unit carries a keyframe NAL
}

// Push consumes one RTP payload. When marker is true and an access unit has
// accumulated, it returns the assembled Annex-B access unit, whether it
// contains a keyframe (IDR/SPS/PPS), and complete=true. Otherwise complete is
// false and au is nil.
func (d *H264Depacketizer) Push(payload []byte, marker bool) (au []byte, keyframe, complete bool) {
	if len(payload) == 0 {
		return d.flush(marker)
	}
	switch nalType := payload[0] & 0x1F; {
	case nalType >= 1 && nalType <= 23:
		d.appendNAL(payload)
	case nalType == naluTypeSTAPA:
		buf := payload[1:]
		for len(buf) >= 2 {
			size := int(buf[0])<<8 | int(buf[1])
			buf = buf[2:]
			if size == 0 || size > len(buf) {
				break
			}
			d.appendNAL(buf[:size])
			buf = buf[size:]
		}
	case nalType == naluTypeFUA:
		if len(payload) < 2 {
			break
		}
		fuHeader := payload[1]
		start := fuHeader&0x80 != 0
		end := fuHeader&0x40 != 0
		if start {
			reconstructed := (payload[0] & 0xE0) | (fuHeader & 0x1F)
			d.fu = append(d.fu[:0], reconstructed)
			d.fuHdr = true
		}
		if d.fuHdr {
			d.fu = append(d.fu, payload[2:]...)
		}
		if end && d.fuHdr {
			d.appendNAL(d.fu)
			d.fu = d.fu[:0]
			d.fuHdr = false
		}
	}
	return d.flush(marker)
}

func (d *H264Depacketizer) appendNAL(nal []byte) {
	if len(nal) == 0 {
		return
	}
	switch nal[0] & 0x1F {
	case 5, 7, 8: // IDR slice, SPS, PPS
		d.hasKey = true
	}
	d.au = append(d.au, 0, 0, 0, 1)
	d.au = append(d.au, nal...)
}

func (d *H264Depacketizer) flush(marker bool) ([]byte, bool, bool) {
	if !marker || len(d.au) == 0 {
		return nil, false, false
	}
	au := d.au
	key := d.hasKey
	d.au = nil
	d.hasKey = false
	return au, key, true
}
