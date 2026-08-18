export type SessionState =
  | "connecting"
  | "qr"
  // Um código de 8 dígitos foi emitido e está aguardando ser digitado no telefone.
  | "pair_code"
  | "open"
  | "logged_out";

export type SessionInfo = {
  id: string;
  name: string;
  jid: string;
  state: SessionState;
  paired: boolean;
  // Presente só enquanto um pareamento por código está em andamento; excludente com o QR.
  code?: string;
};
