import { apiGet, apiGetBlob } from "@/lib/api";
import type { Recording } from "@/types/recording";

// Recordings are stored per call ID across every session, so this listing is not
// session-scoped — match it against a history row by callId.
export const fetchRecordings = () =>
  apiGet<{ recordings: Recording[] }>("/api/recordings");

export const downloadRecording = async (callId: string) => {
  const blob = await apiGetBlob(`/api/recordings/${encodeURIComponent(callId)}`);
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = `call-${callId}.wav`;
  a.click();
  URL.revokeObjectURL(url);
};
