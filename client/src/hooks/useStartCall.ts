import { useMutation } from "@tanstack/react-query";
import { toast } from "sonner";
import { getTransport } from "@/lib/transport";
import { openCall } from "@/lib/webrtc";
import { openWSCall } from "@/lib/ws-call";
import { startCall } from "@/services/calls";
import { registerOwnConnection } from "@/stores/calls";

export const useStartCall = (sid: string, micId: string | null) =>
  useMutation({
    mutationFn: async (vars: { phone: string; isVideo?: boolean }) => {
      // O transporte por WebSocket não carrega vídeo, então uma chamada de vídeo
      // continua no WebRTC mesmo com o fallback escolhido.
      const useWS = getTransport() === "ws" && !vars.isVideo;
      const isVideo = vars.isVideo ?? false;
      const { call } = await startCall(sid, vars.phone, isVideo);
      const conn = useWS
        ? await openWSCall(sid, call.callId, micId)
        : await openCall(sid, call.callId, micId, isVideo);
      registerOwnConnection(call.callId, conn);
      return call.callId;
    },
    onError: (e: Error) => {
      const m = e.message;
      if (m.includes("429"))
        toast.error("Limit reached: max concurrent calls.");
      else if (m.includes("503")) toast.error("WhatsApp not paired.");
      else toast.error(m);
    },
  });
