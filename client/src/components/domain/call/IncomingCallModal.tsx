import { useEffect } from "react";
import { Phone, PhoneOff, Video } from "lucide-react";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { PeerAvatar } from "@/components/domain/contacts/PeerAvatar";
import { useCalls } from "@/stores/calls";
import { useDevices } from "@/stores/devices";
import { useAcceptCall } from "@/hooks/useAcceptCall";
import { useRejectCall } from "@/hooks/useRejectCall";
import { useT } from "@/hooks/useT";

type RingHandle = { stop: () => void };

const startRingLoop = (): RingHandle | null => {
  const AC =
    window.AudioContext ||
    (window as unknown as { webkitAudioContext?: typeof AudioContext })
      .webkitAudioContext;
  if (!AC) return null;
  let ctx: AudioContext;
  try {
    ctx = new AC();
  } catch {
    return null;
  }
  let cancelled = false;
  const playToneAt = (
    when: number,
    durationSec: number,
    freq: number,
    gainVal = 0.18,
  ) => {
    const osc = ctx.createOscillator();
    const gain = ctx.createGain();
    osc.type = "sine";
    osc.frequency.value = freq;
    const t = ctx.currentTime + when;
    gain.gain.setValueAtTime(0, t);
    gain.gain.linearRampToValueAtTime(gainVal, t + 0.02);
    gain.gain.linearRampToValueAtTime(gainVal, t + durationSec - 0.02);
    gain.gain.linearRampToValueAtTime(0, t + durationSec);
    osc.connect(gain).connect(ctx.destination);
    osc.start(t);
    osc.stop(t + durationSec + 0.05);
  };
  const scheduleCycle = () => {
    if (cancelled) return;
    playToneAt(0, 1.0, 440);
    playToneAt(0, 1.0, 480);
    setTimeout(scheduleCycle, 3000);
  };
  scheduleCycle();
  return {
    stop: () => {
      cancelled = true;
      void ctx.close().catch(() => {});
    },
  };
};

export const IncomingCallModal = () => {
  const incoming = useCalls((s) => s.incoming);
  const pendingTransfer = useCalls((s) => s.pendingTransfer);
  const micId = useDevices((s) => s.micId);
  const accept = useAcceptCall(micId);
  const reject = useRejectCall();
  const busy = accept.isPending || reject.isPending;
  const t = useT();

  const activeCall = incoming || (pendingTransfer ? {
    sessionId: pendingTransfer.sessionId,
    callId: pendingTransfer.callId,
    peerName: `Transferência de ${pendingTransfer.fromOwner}`,
    peer: pendingTransfer.callId,
    peerPhotoUrl: undefined,
    isVideo: false,
  } : null);

  useEffect(() => {
    if (!activeCall) return;
    const ring = startRingLoop();
    return () => ring?.stop();
  }, [activeCall]);

  return (
    <Dialog open={!!activeCall}>
      <DialogContent
        showCloseButton={false}
        onEscapeKeyDown={(e) => e.preventDefault()}
        onPointerDownOutside={(e) => e.preventDefault()}
        onInteractOutside={(e) => e.preventDefault()}
        className="sm:max-w-sm"
      >
        <DialogHeader className="items-center text-center">
          <div className="mb-2">
            <PeerAvatar
              name={activeCall?.peerName || activeCall?.peer || ""}
              photoUrl={activeCall?.peerPhotoUrl}
            />
          </div>
          <DialogTitle>{pendingTransfer ? "Transferência de Chamada" : t.incoming.title}</DialogTitle>
          <DialogDescription className="truncate">
            {activeCall?.peerName || activeCall?.peer}
          </DialogDescription>
          {activeCall?.isVideo && (
            <div className="text-muted-foreground mt-1 flex items-center justify-center gap-1 text-xs">
              <Video className="h-3.5 w-3.5" />
              {t.incoming.video}
            </div>
          )}
        </DialogHeader>
        <div className="mt-2 flex items-center justify-center gap-6">
          <Button
            variant="destructive"
            size="icon"
            className="h-14 w-14 rounded-full"
            disabled={busy}
            onClick={() => {
              if (incoming) {
                reject.mutate({
                  sid: incoming.sessionId,
                  callId: incoming.callId,
                });
              } else if (pendingTransfer) {
                useCalls.setState({ pendingTransfer: null });
              }
            }}
            aria-label={t.incoming.reject}
          >
            <PhoneOff className="h-6 w-6" />
          </Button>
          <Button
            size="icon"
            className="h-14 w-14 rounded-full"
            disabled={busy}
            onClick={() => {
              if (activeCall) {
                accept.mutate({
                  sid: activeCall.sessionId,
                  callId: activeCall.callId,
                  isVideo: activeCall.isVideo,
                });
                if (pendingTransfer) {
                  useCalls.setState({ pendingTransfer: null });
                }
              }
            }}
            aria-label={t.incoming.accept}
          >
            {activeCall?.isVideo ? (
              <Video className="h-6 w-6" />
            ) : (
              <Phone className="h-6 w-6" />
            )}
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  );
};
