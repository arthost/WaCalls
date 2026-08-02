import {
  VIDEO_BITRATE,
  VIDEO_CHANNEL_LABEL,
  VIDEO_CLOCK_HZ,
  VIDEO_FPS,
  VIDEO_HEIGHT,
  VIDEO_WIDTH,
} from "@/constants/audio";

export type VideoChannel = {
  // Always present: the peer can push video at any time (mid-call upgrade), so
  // the inbound decode path is wired up front regardless of our own camera.
  remoteStream: MediaStream;
  // The local camera stream, populated only after startCapture() succeeds.
  localStream: MediaStream | null;
  // startCapture opens the camera + encoder and begins sending framed Annex-B.
  // Returns the local MediaStream (for a self-view) or null if unavailable.
  startCapture: () => Promise<MediaStream | null>;
  // stopCapture tears the camera + encoder down but keeps the channel and the
  // inbound decode path alive, so we can still receive the peer's video.
  stopCapture: () => void;
  close: () => void;
};

// Wire framing shared with the Go bridge (bridge.go videoFrameHeaderLen = 5):
// 4-byte big-endian 90 kHz timestamp, 1-byte keyframe flag, then Annex-B bytes.
const HEADER_LEN = 5;

const usToTs90 = (us: number) => Math.round(us * (VIDEO_CLOCK_HZ / 1_000_000)) >>> 0;
const ts90ToUs = (ts90: number) => Math.round(ts90 * (1_000_000 / VIDEO_CLOCK_HZ));

const hasWebCodecs = () =>
  typeof (globalThis as unknown as { VideoEncoder?: unknown }).VideoEncoder !==
    "undefined" &&
  typeof (globalThis as unknown as { VideoDecoder?: unknown }).VideoDecoder !==
    "undefined" &&
  typeof (
    globalThis as unknown as { MediaStreamTrackProcessor?: unknown }
  ).MediaStreamTrackProcessor !== "undefined" &&
  typeof (
    globalThis as unknown as { MediaStreamTrackGenerator?: unknown }
  ).MediaStreamTrackGenerator !== "undefined";

// setupVideoChannel wires the H.264 "video" data channel. The inbound decode
// path (peer Annex-B → renderable MediaStream) is always active so a mid-call
// upgrade from the peer is rendered even when our own camera stays off. Outbound
// capture is deferred to startCapture() so the camera is only opened on demand.
// Returns null when WebCodecs is unavailable, so callers fall back to audio-only.
export const setupVideoChannel = (
  pc: RTCPeerConnection,
): VideoChannel | null => {
  if (!hasWebCodecs()) {
    console.warn("WebCodecs unavailable; video disabled");
    return null;
  }

  const dc = pc.createDataChannel(VIDEO_CHANNEL_LABEL, { ordered: true });
  dc.binaryType = "arraybuffer";

  // ---- Inbound (always on): decode peer Annex-B into a renderable stream ----
  const generator = new MediaStreamTrackGenerator({ kind: "video" });
  const writer = generator.writable.getWriter();
  const remoteStream = new MediaStream([generator]);

  let decoder: VideoDecoder | null = null;
  const ensureDecoder = () => {
    if (decoder) return decoder;
    decoder = new VideoDecoder({
      output: (frame) => {
        writer.write(frame).catch(() => {});
        frame.close();
      },
      error: (e) => console.warn("video decoder error", e),
    });
    // Annex-B keyframes carry SPS/PPS inline, so no description is needed.
    decoder.configure({
      codec: "avc1.42e01f",
      optimizeForLatency: true,
    } as VideoDecoderConfig);
    return decoder;
  };

  dc.onmessage = (e: MessageEvent<ArrayBuffer>) => {
    const buf = new Uint8Array(e.data);
    if (buf.byteLength <= HEADER_LEN) return;
    const view = new DataView(e.data);
    const ts90 = view.getUint32(0, false);
    const keyframe = buf[4] === 1;
    const annexb = buf.subarray(HEADER_LEN);
    const dec = ensureDecoder();
    if (dec.state !== "configured") return;
    try {
      dec.decode(
        new EncodedVideoChunk({
          type: keyframe ? "key" : "delta",
          timestamp: ts90ToUs(ts90),
          data: annexb,
        }),
      );
    } catch (err) {
      console.warn("video decode failed", err);
    }
  };

  // ---- Outbound (on demand): opened by startCapture() ----
  let localStream: MediaStream | null = null;
  let encoder: VideoEncoder | null = null;
  let reader: ReadableStreamDefaultReader<VideoFrame> | null = null;
  let capturing = false;

  const startCapture = async (): Promise<MediaStream | null> => {
    if (capturing && localStream) return localStream;
    try {
      localStream = await navigator.mediaDevices.getUserMedia({
        video: {
          width: { ideal: VIDEO_WIDTH },
          height: { ideal: VIDEO_HEIGHT },
          frameRate: { ideal: VIDEO_FPS },
        },
        audio: false,
      });
    } catch (err) {
      console.warn("camera unavailable; video disabled", err);
      return null;
    }

    const [track] = localStream.getVideoTracks();
    const processor = new MediaStreamTrackProcessor({ track });
    reader = processor.readable.getReader();

    encoder = new VideoEncoder({
      output: (chunk) => {
        if (dc.readyState !== "open") return;
        const data = new Uint8Array(chunk.byteLength);
        chunk.copyTo(data);
        const msg = new Uint8Array(HEADER_LEN + data.byteLength);
        new DataView(msg.buffer).setUint32(0, usToTs90(chunk.timestamp), false);
        msg[4] = chunk.type === "key" ? 1 : 0;
        msg.set(data, HEADER_LEN);
        try {
          dc.send(msg);
        } catch {}
      },
      error: (e) => console.warn("video encoder error", e),
    });
    encoder.configure({
      codec: "avc1.42e01f",
      width: VIDEO_WIDTH,
      height: VIDEO_HEIGHT,
      bitrate: VIDEO_BITRATE,
      framerate: VIDEO_FPS,
      latencyMode: "realtime",
      avc: { format: "annexb" },
    } as VideoEncoderConfig);

    capturing = true;
    let frameCount = 0;
    const pump = async () => {
      while (capturing && reader) {
        const { value: frame, done } = await reader.read();
        if (done || !frame) break;
        if (
          encoder &&
          encoder.state === "configured" &&
          encoder.encodeQueueSize < 2
        ) {
          // Force a keyframe roughly twice a second so a late/dropped decoder can
          // resync — the mobile leg has no way to send us a PLI over this path.
          const keyFrame = frameCount % (VIDEO_FPS * 2) === 0;
          encoder.encode(frame, { keyFrame });
          frameCount++;
        }
        frame.close();
      }
    };
    pump().catch((err) => console.warn("video capture pump ended", err));
    return localStream;
  };

  const stopCapture = () => {
    capturing = false;
    try {
      reader?.cancel();
    } catch {}
    reader = null;
    try {
      if (encoder && encoder.state !== "closed") encoder.close();
    } catch {}
    encoder = null;
    try {
      localStream?.getTracks().forEach((t) => t.stop());
    } catch {}
    localStream = null;
  };

  return {
    remoteStream,
    get localStream() {
      return localStream;
    },
    startCapture,
    stopCapture,
    close: () => {
      stopCapture();
      try {
        if (decoder && decoder.state !== "closed") decoder.close();
      } catch {}
      try {
        writer.close();
      } catch {}
    },
  };
};
