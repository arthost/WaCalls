import { apiGet, apiPost, apiDelete, prefixPath } from "@/lib/api";
import { getClientId } from "@/lib/client-id";
import type { SessionInfo } from "@/types/session";

export const listSessions = () =>
  apiGet<{ sessions: SessionInfo[] }>("/api/sessions").then(
    (r) => r.sessions ?? [],
  );

export const createSession = (name: string) =>
  apiPost<{ id: string }>("/api/sessions", { name });

export const deleteSession = (id: string) => apiDelete(`/api/sessions/${id}`);

const postVoid = async (path: string): Promise<void> => {
  const r = await fetch(prefixPath(path), {
    method: "POST",
    headers: {
      "X-Client-Id": getClientId(),
      "Content-Type": "application/json",
    },
    body: "{}",
    credentials: "same-origin",
  });
  if (!r.ok) throw new Error(`${path} ${r.status}`);
};

export const logoutSession = (id: string) =>
  postVoid(`/api/sessions/${id}/logout`);

export const pairSession = (id: string) => postVoid(`/api/sessions/${id}/pair`);

// pairSessionByPhone inicia o pareamento por código de 8 dígitos ("Conectar com número
// de telefone" no aparelho), para quando o telefone não pode ver a tela do navegador.
// phone vai em formato internacional, código do país primeiro.
export const pairSessionByPhone = (id: string, phone: string) =>
  apiPost<{ code: string }>(`/api/sessions/${id}/pair-code`, { phone });
