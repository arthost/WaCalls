import { apiPost } from "./api";
import { setupAudioChannel } from "./call/audio-channel";
import { setupVideoChannel, type VideoChannel } from "./call/video-channel";

export type OpenCall = {
  pc: RTCPeerConnection;
  micStream: MediaStream;
  remoteStream: MediaStream | null;
  // Decoded peer video — always present when WebCodecs is available, since the
  // peer can push video mid-call even on a call that started as audio.
  remoteVideoStream: MediaStream | null;
  // Local camera preview, populated only after startVideo() opens the camera.
  readonly localVideoStream: MediaStream | null;
  // Whether our local camera is currently capturing/sending.
  readonly videoActive: boolean;
  // startVideo opens the local camera and begins sending H.264; returns the
  // self-view stream (or null if the camera/WebCodecs is unavailable).
  startVideo: () => Promise<MediaStream | null>;
  // stopVideo tears the camera down but keeps receiving the peer's video.
  stopVideo: () => void;
  close: () => void;
};

export const openCall = async (
  sid: string,
  callId: string,
  micDeviceId: string | null,
  video = false,
): Promise<OpenCall> => {
  const localStream = await navigator.mediaDevices.getUserMedia({
    audio: micDeviceId ? { deviceId: { exact: micDeviceId } } : true,
    video: false,
  });

  const pc = new RTCPeerConnection({ iceServers: [] });
  const audio = await setupAudioChannel(pc, localStream);

  // Always wire the video channel (a dormant SCTP stream is cheap) so a mid-call
  // upgrade — in either direction — needs no WebRTC renegotiation. Only the
  // camera capture is deferred until video is actually enabled.
  const videoChannel: VideoChannel | null = setupVideoChannel(pc);

  const offer = await pc.createOffer();
  await pc.setLocalDescription(offer);
  await new Promise<void>((resolve) => {
    if (pc.iceGatheringState === "complete") resolve();
    else
      pc.addEventListener("icegatheringstatechange", () => {
        if (pc.iceGatheringState === "complete") resolve();
      });
  });

  const { sdp_answer } = await apiPost<{ sdp_answer: string }>(
    `/api/sessions/${sid}/calls/${callId}/webrtc`,
    { sdp_offer: pc.localDescription!.sdp },
  );
  await pc.setRemoteDescription({ type: "answer", sdp: sdp_answer });

  let videoActive = false;

  const call: OpenCall = {
    pc,
    micStream: localStream,
    remoteStream: audio.remoteStream,
    remoteVideoStream: videoChannel?.remoteStream ?? null,
    get localVideoStream() {
      return videoChannel?.localStream ?? null;
    },
    get videoActive() {
      return videoActive;
    },
    startVideo: async () => {
      if (!videoChannel) return null;
      const stream = await videoChannel.startCapture();
      videoActive = stream !== null;
      return stream;
    },
    stopVideo: () => {
      videoChannel?.stopCapture();
      videoActive = false;
    },
    close: () => {
      audio.close();
      videoChannel?.close();
      try {
        localStream.getTracks().forEach((t) => t.stop());
      } catch {}
      try {
        pc.close();
      } catch {}
    },
  };

  // A call that starts as video opens the camera immediately.
  if (video) {
    await call.startVideo();
  }

  return call;
};
