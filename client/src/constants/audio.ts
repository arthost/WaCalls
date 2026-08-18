export const SAMPLE_RATE = 16000;
export const PCM_CHANNEL_LABEL = "pcm";
// Subprotocol for the WebSocket audio transport (the WebRTC fallback). It names the
// wire format the server expects: raw Int16 LE PCM at SAMPLE_RATE, mono, one binary
// message per frame — the same currency PCM_CHANNEL_LABEL carries.
export const WS_AUDIO_SUBPROTOCOL = "pcm16";
export const CAPTURE_WORKLET_URL = "/worklets/capture-processor.js";
export const PLAYBACK_WORKLET_URL = "/worklets/playback-processor.js";
export const CAPTURE_PROCESSOR_NAME = "capture-processor";
export const PLAYBACK_PROCESSOR_NAME = "playback-processor";

// Video (WebCodecs H.264 over a data channel). The Go side does no video codec;
// it only packetizes the browser-encoded Annex-B into RTP PT 97.
export const VIDEO_CHANNEL_LABEL = "video";
// 90 kHz RTP video clock; WebCodecs timestamps are microseconds, so multiply by
// 90000/1e6 = 0.09 to convert to the wire clock.
export const VIDEO_CLOCK_HZ = 90000;
export const VIDEO_WIDTH = 640;
export const VIDEO_HEIGHT = 480;
export const VIDEO_FPS = 20;
// Conservative bitrate — the WhatsApp mobile leg caps inbound hard, but outbound
// (CRM → mobile) is not capped, so we keep this modest but not starved.
export const VIDEO_BITRATE = 600_000;
