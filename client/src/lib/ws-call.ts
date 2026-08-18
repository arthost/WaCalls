import { wsUrl } from "./api";
import { setupWSAudioChannel } from "./call/ws-audio";
import type { OpenCall } from "./webrtc";

/**
 * openWSCall é o openCall do transporte por WebSocket: mesma forma de OpenCall, então
 * a UI e o store não distinguem um do outro. Só áudio — vídeo continua exigindo o
 * WebRTC, porque o H.264 do WebCodecs anda no data channel de vídeo. As operações de
 * vídeo são no-ops silenciosas em vez de erros: o botão de câmera some sozinho quando
 * videoActive nunca fica verdadeiro, e um throw aqui só produziria um toast inútil.
 *
 * onDrop encerra a chamada no servidor quando o socket cai; o servidor faz o mesmo do
 * seu lado, então quem chegar primeiro basta.
 */
export const openWSCall = async (
  sid: string,
  callId: string,
  micDeviceId: string | null,
): Promise<OpenCall> => {
  let localStream: MediaStream;
  try {
    localStream = await navigator.mediaDevices.getUserMedia({
      audio: micDeviceId ? { deviceId: { exact: micDeviceId } } : true,
      video: false,
    });
  } catch (err) {
    if (micDeviceId) {
      localStream = await navigator.mediaDevices.getUserMedia({
        audio: true,
        video: false,
      });
    } else {
      throw err;
    }
  }

  const stopMic = () => {
    try {
      localStream.getTracks().forEach((t) => t.stop());
    } catch {}
  };

  let audio;
  try {
    audio = await setupWSAudioChannel(
      wsUrl(`/api/sessions/${sid}/calls/${callId}/ws`),
      localStream,
      () => {
        // O socket caiu por conta própria. O servidor já está encerrando a chamada
        // WhatsApp; aqui só liberamos o microfone para o LED da câmera/mic apagar.
        stopMic();
      },
    );
  } catch (err) {
    stopMic();
    throw err;
  }

  return {
    pc: null,
    micStream: localStream,
    remoteStream: audio.remoteStream,
    remoteVideoStream: null,
    get localVideoStream() {
      return null;
    },
    get videoActive() {
      return false;
    },
    startVideo: async () => null,
    startScreenShare: async () => null,
    stopVideo: () => {},
    close: () => {
      audio.close();
      stopMic();
    },
  };
};
