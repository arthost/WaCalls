package signaling

import (
	"fmt"
	"wacalls/internal/voip/wanode"

	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/types"
)

// Capability blobs para chamadas de VÍDEO. O byte de índice 5 é o media-type:
// 0xbb = áudio (ver capabilityOffer/capabilityPreaccept em signaling_build.go),
// 0xfa = vídeo. Sem trocar esse byte E anexar um nó <video>, o WhatsApp toca a
// chamada como áudio no outro lado. Valores conferidos contra a referência
// WaCallsNative (feat/video-signaling).
var (
	capabilityVideoOffer     = []byte{0x01, 0x05, 0xf7, 0x09, 0xe4, 0xfa, 0x13}
	capabilityVideoPreaccept = []byte{0x01, 0x05, 0xff, 0x09, 0xe0, 0xfa, 0x13}
)

// videoOfferNode monta o nó <video> anexado ao <offer> quando a chamada é de
// vídeo. As dimensões/orientação seguem o cliente oficial; o codec anunciado é
// H264 (nosso transporte é datachannel H264 + WebCodecs).
func videoOfferNode() waBinary.Node {
	return waBinary.Node{
		Tag: "video",
		Attrs: waBinary.Attrs{
			"enc":                "h.264",
			"dec":                "H264",
			"screen_width":       "1920",
			"screen_height":      "1080",
			"device_orientation": "0",
		},
	}
}

// videoAcceptNode monta o nó <video> anexado ao <accept>/<preaccept> quando
// aceitamos uma chamada de vídeo. Anunciamos os decoders suportados.
func videoAcceptNode() waBinary.Node {
	return waBinary.Node{
		Tag:   "video",
		Attrs: waBinary.Attrs{"dec": "H264,H265,AV1"},
	}
}

// OfferHasVideo reporta se o nó interno (<offer>/<accept>) carrega um <video>,
// o que indica que a contraparte quer uma chamada de vídeo (e não áudio).
func OfferHasVideo(inner *waBinary.Node) bool {
	if inner == nil {
		return false
	}
	for _, c := range wanode.NodeChildren(inner) {
		if c.Tag == "video" {
			return true
		}
	}
	return false
}

// OfferVideoOrientation extrai device_orientation do nó <video>, ou 0.
func OfferVideoOrientation(inner *waBinary.Node) int {
	if inner == nil {
		return 0
	}
	for _, c := range wanode.NodeChildren(inner) {
		if c.Tag == "video" {
			return wanode.AttrInt(c.Attrs, "device_orientation", 0)
		}
	}
	return 0
}

// Estados da negociação de vídeo mid-call, carregados no atributo "state" do nó
// <video> dentro de um <call>. Fonte: referência WaCallsNative.
const (
	VideoStateDisabled       = 0
	VideoStateEnabled        = 1
	VideoStateUpgradeRequest = 3
	VideoStateUpgradeAccept  = 4
	VideoStateUpgradeReject  = 5
	VideoStateStopped        = 6
	VideoStateUpgradeCancel  = 8
	VideoStateUpgradeReqV2   = 11
)

// VideoStateParams descreve uma stanza <call><video state=N>.
type VideoStateParams struct {
	PeerJid     types.JID
	CallID      string
	CallCreator types.JID
	State       int
}

// BuildVideoStateStanza monta o <call> mid-call que sinaliza um estado de vídeo
// (usado no upgrade áudio→vídeo). A contraparte deve responder com um ACK
// tipado (ver BuildVideoAck); um ack simples faz o WhatsApp cancelar em ~5s.
func BuildVideoStateStanza(p VideoStateParams) waBinary.Node {
	return waBinary.Node{
		Tag:   "call",
		Attrs: waBinary.Attrs{"to": p.PeerJid, "id": GenerateCallStanzaID()},
		Content: []waBinary.Node{{
			Tag: "video",
			Attrs: waBinary.Attrs{
				"call-id":      p.CallID,
				"call-creator": p.CallCreator,
				"state":        fmt.Sprintf("%d", p.State),
			},
		}},
	}
}

// BuildVideoAck monta o ACK TIPADO (type="video") exigido para stanzas de vídeo
// mid-call. callNode é o <call> recebido (para extrair id/from).
func BuildVideoAck(callNode *waBinary.Node) waBinary.Node {
	id := wanode.AttrString(callNode.Attrs, "id")
	from := wanode.AttrString(callNode.Attrs, "from")
	return waBinary.Node{
		Tag: "ack",
		Attrs: waBinary.Attrs{
			"id":    id,
			"to":    wanode.MustJID(from),
			"class": "call",
			"type":  "video",
		},
	}
}

// ParsedVideoState é o resultado de ler um <call><video state=N> recebido.
type ParsedVideoState struct {
	CallID      string
	CallCreator string
	State       int
	Found       bool
}

// ParseVideoState extrai o <video state=N> de um <call> recebido, se houver.
func ParseVideoState(callNode *waBinary.Node) ParsedVideoState {
	for _, c := range wanode.NodeChildren(callNode) {
		if c.Tag != "video" {
			continue
		}
		return ParsedVideoState{
			CallID:      wanode.AttrString(c.Attrs, "call-id"),
			CallCreator: wanode.AttrString(c.Attrs, "call-creator"),
			State:       wanode.AttrInt(c.Attrs, "state", 0),
			Found:       true,
		}
	}
	return ParsedVideoState{}
}
