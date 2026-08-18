export type Transport = "webrtc" | "ws";

const STORAGE_KEY = "duocrm.calls.transport";

const read = (): Transport | null => {
  try {
    const v = window.localStorage.getItem(STORAGE_KEY);
    return v === "ws" || v === "webrtc" ? v : null;
  } catch {
    // localStorage throws in private modes / with storage blocked.
    return null;
  }
};

const write = (t: Transport): void => {
  try {
    window.localStorage.setItem(STORAGE_KEY, t);
  } catch {}
};

/**
 * getTransport escolhe o transporte de mídia das próximas chamadas.
 *
 * WebRTC é o padrão: latência menor e é o único que carrega vídeo. O transporte por
 * WebSocket existe para o operador cuja rede descarta UDP (ou cujo proxy não tem
 * caminho UDP) — ali o WebRTC simplesmente nunca conecta, e áudio por TCP é melhor
 * que chamada nenhuma.
 *
 * A escolha é sempre explícita, nunca automática: `?transport=ws` na URL vence e é
 * memorizada, então o operador orientado a "acrescente ?transport=ws" não precisa
 * repetir isso a cada acesso. `?transport=webrtc` volta ao padrão.
 */
export const getTransport = (): Transport => {
  const q = new URLSearchParams(window.location.search).get("transport");
  if (q === "ws" || q === "webrtc") {
    write(q);
    return q;
  }
  return read() ?? "webrtc";
};

export const setTransport = (t: Transport): void => write(t);
