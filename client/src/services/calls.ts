import { apiPost, apiDelete } from "@/lib/api";

export const startCall = (sid: string, phone: string, isVideo = false) =>
  apiPost<{ call: { callId: string } }>(`/api/sessions/${sid}/calls`, {
    phone,
    is_video: isVideo,
  });

export const acceptCall = (sid: string, callId: string) =>
  apiPost<{ call: { callId: string } }>(
    `/api/sessions/${sid}/calls/${callId}/accept`,
    {},
  );

export const rejectCall = (sid: string, callId: string) =>
  apiPost<{ status: string }>(
    `/api/sessions/${sid}/calls/${callId}/reject`,
    {},
  );

export const endCall = (sid: string, callId: string) =>
  apiDelete(`/api/sessions/${sid}/calls/${callId}`);

export const enableVideo = (sid: string, callId: string) =>
  apiPost<{ status: string }>(`/api/sessions/${sid}/calls/${callId}/video`, {
    action: "enable",
  });

// disableVideo avisa o par que paramos de enviar vídeo (<video state=6>), para a UI
// dele soltar nosso tile em vez de congelar no último frame decodificado. Só a direção
// de saída para: o par pode continuar enviando.
export const disableVideo = (sid: string, callId: string) =>
  apiPost<{ status: string }>(`/api/sessions/${sid}/calls/${callId}/video`, {
    action: "stop",
  });

export const holdCall = (sid: string, callId: string) =>
  apiPost<{ status: string }>(`/api/sessions/${sid}/calls/${callId}/hold`, {
    action: "hold",
  });

export const resumeCall = (sid: string, callId: string) =>
  apiPost<{ status: string }>(`/api/sessions/${sid}/calls/${callId}/hold`, {
    action: "resume",
  });

export const transferCall = (sid: string, callId: string, toOwner: string) =>
  apiPost<{ status: string }>(`/api/sessions/${sid}/calls/${callId}/transfer`, {
    to: toOwner,
  });

export const setRecording = (sid: string, callId: string, on: boolean) =>
  apiPost<{ status: string }>(`/api/sessions/${sid}/calls/${callId}/record`, {
    action: on ? "start" : "stop",
  });
