import { prefixPath } from "@/lib/api";
import { float32ToInt16LE, int16LEToFloat32 } from "@/lib/pcm";
import {
  CAPTURE_PROCESSOR_NAME,
  CAPTURE_WORKLET_URL,
  PLAYBACK_PROCESSOR_NAME,
  PLAYBACK_WORKLET_URL,
  SAMPLE_RATE,
  WS_AUDIO_SUBPROTOCOL,
} from "@/constants/audio";

export type WSAudioChannel = {
  remoteStream: MediaStream;
  close: () => void;
};

/**
 * setupWSAudioChannel é o gêmeo de setupAudioChannel sobre um WebSocket simples,
 * para o operador em rede que não deixa o WebRTC conectar. Os worklets de captura e
 * de playback são exatamente os mesmos — inclusive o ring buffer de 2 s do playback,
 * que já absorve o jitter —, só o cano por onde o PCM passa muda.
 *
 * onDrop é chamado apenas quando o socket cai sozinho (rede, proxy, servidor); um
 * close() local não o dispara, senão encerrar a chamada a encerraria duas vezes.
 */
export const setupWSAudioChannel = async (
  url: string,
  micStream: MediaStream,
  onDrop: () => void,
): Promise<WSAudioChannel> => {
  const ws = new WebSocket(url, WS_AUDIO_SUBPROTOCOL);
  ws.binaryType = "arraybuffer";

  await new Promise<void>((resolve, reject) => {
    const settle = (fn: () => void) => () => {
      ws.removeEventListener("open", onOpen);
      ws.removeEventListener("error", onFail);
      ws.removeEventListener("close", onFail);
      fn();
    };
    const onOpen = settle(resolve);
    // O handshake não expõe o status HTTP ao JS: 401/404/400 chegam aqui como um
    // close silencioso, então a mensagem é deliberadamente genérica.
    const onFail = settle(() =>
      reject(new Error("call websocket failed to open")),
    );
    ws.addEventListener("open", onOpen);
    ws.addEventListener("error", onFail);
    ws.addEventListener("close", onFail);
  });

  let closing = false;
  const close = () => {
    closing = true;
    try {
      ws.close();
    } catch {}
  };

  let ctx: AudioContext;
  try {
    ctx = new AudioContext({ sampleRate: SAMPLE_RATE });
    await ctx.audioWorklet.addModule(prefixPath(CAPTURE_WORKLET_URL));
    await ctx.audioWorklet.addModule(prefixPath(PLAYBACK_WORKLET_URL));
    await ctx.resume();
  } catch (err) {
    // O socket já está aberto neste ponto; sem isto o servidor ficaria com uma
    // chamada presa a um operador que nunca vai falar.
    close();
    throw err;
  }

  const micSource = ctx.createMediaStreamSource(micStream);
  const captureNode = new AudioWorkletNode(ctx, CAPTURE_PROCESSOR_NAME);
  captureNode.port.onmessage = (e: MessageEvent<Float32Array>) => {
    if (ws.readyState === WebSocket.OPEN) ws.send(float32ToInt16LE(e.data));
  };
  micSource.connect(captureNode);
  captureNode.connect(ctx.destination);

  const playbackNode = new AudioWorkletNode(ctx, PLAYBACK_PROCESSOR_NAME);
  const streamDest = ctx.createMediaStreamDestination();
  playbackNode.connect(streamDest);
  ws.onmessage = (e: MessageEvent<ArrayBuffer | string>) => {
    if (typeof e.data === "string") return;
    playbackNode.port.postMessage(int16LEToFloat32(e.data));
  };
  ws.onclose = () => {
    if (!closing) onDrop();
  };

  return {
    remoteStream: streamDest.stream,
    close: () => {
      close();
      try {
        ctx.close();
      } catch {}
    },
  };
};
