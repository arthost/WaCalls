// Ambient declarations for the MediaStreamTrack Insertable Streams API
// (a.k.a. Breakout Box). These are shipped in Chromium but not yet in
// TypeScript's DOM lib, so we declare the minimal surface we use in
// lib/call/video-channel.ts. See https://w3c.github.io/mediacapture-transform/.

interface MediaStreamTrackProcessorInit {
  track: MediaStreamTrack;
  maxBufferSize?: number;
}

declare class MediaStreamTrackProcessor<T = VideoFrame> {
  constructor(init: MediaStreamTrackProcessorInit);
  readonly readable: ReadableStream<T>;
}

interface MediaStreamTrackGeneratorInit {
  kind: "video" | "audio";
}

declare class MediaStreamTrackGenerator<T = VideoFrame>
  extends MediaStreamTrack {
  constructor(init: MediaStreamTrackGeneratorInit);
  readonly writable: WritableStream<T>;
}
