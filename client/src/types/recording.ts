export type Recording = {
  callId: string;
  size: number;
  modifiedAt: number;
  // Still being written: the WAV length field is only patched when the call ends,
  // so downloading now yields a file players read as empty.
  active: boolean;
};
