import { useMutation } from "@tanstack/react-query";
import { toast } from "sonner";
import { getTransport } from "@/lib/transport";
import { openCall } from "@/lib/webrtc";
import { openWSCall } from "@/lib/ws-call";
import { acceptCall, endCall } from "@/services/calls";
import { registerOwnConnection, clearIncoming } from "@/stores/calls";

export const useAcceptCall = (micId: string | null) =>
  useMutation({
    mutationFn: async (vars: {
      sid: string;
      callId: string;
      isVideo?: boolean;
    }) => {
      // O transporte por WebSocket não carrega vídeo, então uma chamada de vídeo
      // continua no WebRTC mesmo com o fallback escolhido.
      const useWS = getTransport() === "ws" && !vars.isVideo;
      const res = await acceptCall(vars.sid, vars.callId);
      try {
        const conn = useWS
          ? await openWSCall(vars.sid, res.call.callId, micId)
          : await openCall(
              vars.sid,
              res.call.callId,
              micId,
              vars.isVideo ?? false,
            );
        registerOwnConnection(res.call.callId, conn);
      } catch (wrtcErr) {
        try {
          await endCall(vars.sid, res.call.callId);
        } catch {}
        throw wrtcErr;
      }
      clearIncoming();
      return res.call.callId;
    },
    onError: (e: Error) => {
      if (e.message.includes("409")) {
        clearIncoming();
        return;
      }
      toast.error(e.message);
    },
  });
