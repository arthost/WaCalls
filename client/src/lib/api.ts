import { getClientId } from "./client-id";

let onUnauthorized: () => void = () => {};
export const setOnUnauthorized = (fn: () => void): void => {
  onUnauthorized = fn;
};

// Retorna o prefixo do subcaminho do gateway caso o app esteja embutido sob /api/v1/calls
export const prefixPath = (path: string): string => {
  if (path.startsWith("/")) {
    const base = window.location.pathname.includes("/api/v1/calls") ? "/api/v1/calls" : "";
    return `${base}${path}`;
  }
  return path;
};

// wsUrl converte um caminho da API em uma URL WebSocket absoluta na mesma origem.
// Mesma origem importa: é o que permite ao cookie de sessão (SameSite=Strict)
// acompanhar o handshake — o WebSocket não aceita headers, então sem o cookie a
// única alternativa seria expor a API key na query string.
export const wsUrl = (path: string): string => {
  const scheme = window.location.protocol === "https:" ? "wss:" : "ws:";
  return `${scheme}//${window.location.host}${prefixPath(path)}`;
};

const baseHeaders = (): HeadersInit => ({
  "X-Client-Id": getClientId(),
  "Content-Type": "application/json",
});

const guard = (status: number): void => {
  if (status === 401) {
    onUnauthorized();
  }
};

export const apiGet = async <T>(path: string): Promise<T> => {
  const r = await fetch(prefixPath(path), {
    headers: baseHeaders(),
    credentials: "same-origin",
  });
  if (!r.ok) {
    guard(r.status);
    throw new Error(`${path} ${r.status}`);
  }
  return r.json() as Promise<T>;
};

export const apiGetBlob = async (path: string): Promise<Blob> => {
  const r = await fetch(prefixPath(path), {
    headers: baseHeaders(),
    credentials: "same-origin",
  });
  if (!r.ok) {
    guard(r.status);
    throw new Error(`${path} ${r.status}`);
  }
  return r.blob();
};

export const apiPost = async <T>(path: string, body: unknown): Promise<T> => {
  const r = await fetch(prefixPath(path), {
    method: "POST",
    headers: baseHeaders(),
    body: JSON.stringify(body),
    credentials: "same-origin",
  });
  if (!r.ok) {
    guard(r.status);
    const text = await r.text().catch(() => "");
    throw new Error(`${path} ${r.status} ${text}`);
  }
  if (r.status === 204) return null as T;
  return r.json() as Promise<T>;
};

export const apiDelete = async (path: string): Promise<void> => {
  const r = await fetch(prefixPath(path), {
    method: "DELETE",
    headers: baseHeaders(),
    credentials: "same-origin",
  });
  if (!r.ok) {
    guard(r.status);
    throw new Error(`${path} ${r.status}`);
  }
};
