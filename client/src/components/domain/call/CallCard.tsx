import { useEffect, useRef, useState } from "react";
import { toast } from "sonner";
import { Check, PhoneOff, Video, VideoOff, WifiOff, Pause, Play, Disc, Forward } from "lucide-react";
import { Card, CardContent } from "@/components/ui/card";
import { StatusBadge } from "@/components/ui/status-badge";
import { Button } from "@/components/ui/button";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import { attachMeter } from "@/lib/audio-meter";
import { enableVideo, disableVideo, holdCall, resumeCall, transferCall, setRecording } from "@/services/calls";
import { useCalls } from "@/stores/calls";
import { useDevices } from "@/stores/devices";
import { useEndCall } from "@/hooks/useEndCall";
import { useT } from "@/hooks/useT";
import { callStatusTone, callStatusPulse } from "@/lib/status";
import { PeerAvatar } from "@/components/domain/contacts/PeerAvatar";
import { waveLevel } from "@/lib/waveform";
import { formatCallDuration } from "@/utils/format";
import type {
  CallStatus,
  CallSummary,
  QualitySample,
  SetupMark,
} from "@/types/call";

const waveBars = [
  { h: 8, delay: "0s" },
  { h: 14, delay: "0.12s" },
  { h: 20, delay: "0.24s" },
  { h: 13, delay: "0.12s" },
  { h: 9, delay: "0s" },
];

const Waveform = ({ db }: { db: number }) => (
  <div
    className="flex h-5 items-center gap-[3px]"
    style={{ transform: `scaleY(${waveLevel(db)})` }}
  >
    {waveBars.map((bar, i) => (
      <span
        key={i}
        className="tom-wave w-[2.5px] rounded-full bg-primary"
        style={{ height: `${bar.h}px`, animationDelay: bar.delay }}
      />
    ))}
  </div>
);

const Meter = ({ label, db }: { label: string; db: number }) => (
  <div className="space-y-1">
    <p className="text-xs text-muted-foreground">{label}</p>
    <Waveform db={db} />
  </div>
);

type QualityTone = "ok" | "warn" | "bad" | "idle";

const toneFill: Record<QualityTone, string> = {
  ok: "bg-primary",
  warn: "bg-amber-500",
  bad: "bg-destructive",
  idle: "bg-muted-foreground/40",
};

const toneText: Record<QualityTone, string> = {
  ok: "text-primary",
  warn: "text-amber-600 dark:text-amber-400",
  bad: "text-destructive",
  idle: "text-muted-foreground",
};

const clampPct = (value: number, full: number) =>
  Math.max(0, Math.min(100, (value / full) * 100));

const rttTone = (ms: number): QualityTone =>
  ms <= 150 ? "ok" : ms <= 300 ? "warn" : "bad";
const jitterTone = (ms: number): QualityTone =>
  ms <= 30 ? "ok" : ms <= 50 ? "warn" : "bad";
const lossTone = (frac: number): QualityTone =>
  frac <= 0.01 ? "ok" : frac <= 0.05 ? "warn" : "bad";

const QualityBar = ({
  label,
  value,
  pct,
  tone,
}: {
  label: string;
  value: string;
  pct: number;
  tone: QualityTone;
}) => (
  <div className="space-y-1">
    <div className="flex items-baseline justify-between">
      <span className="text-xs uppercase tracking-wide text-muted-foreground">
        {label}
      </span>
      <span className={`font-mono text-xs font-semibold ${toneText[tone]}`}>
        {value}
      </span>
    </div>
    <div className="h-2 overflow-hidden rounded-full bg-muted">
      <div
        className={`h-full transition-all ${toneFill[tone]}`}
        style={{ width: `${pct}%` }}
      />
    </div>
  </div>
);

const QualityPanel = ({ q }: { q: QualitySample | undefined }) => {
  const t = useT();
  if (!q) {
    return (
      <p className="text-xs text-muted-foreground">
        {t.calls.measuringQuality}
      </p>
    );
  }
  const lossPct = q.lossFraction * 100;
  return (
    <div className="space-y-2 rounded-md border border-border/60 p-2">
      {q.hasRtt ? (
        <QualityBar
          label={t.calls.rtt}
          value={`${Math.round(q.rttMs)} ms`}
          pct={clampPct(q.rttMs, 400)}
          tone={rttTone(q.rttMs)}
        />
      ) : (
        <QualityBar label={t.calls.rtt} value="—" pct={0} tone="idle" />
      )}
      <QualityBar
        label={t.calls.jitter}
        value={`${Math.round(q.jitterMs)} ms`}
        pct={clampPct(q.jitterMs, 80)}
        tone={jitterTone(q.jitterMs)}
      />
      <QualityBar
        label={t.calls.loss}
        value={`${lossPct.toFixed(1)}%`}
        pct={clampPct(lossPct, 10)}
        tone={lossTone(q.lossFraction)}
      />
    </div>
  );
};

const markSteps = [
  { key: "transport.ice", label: "ICE" },
  { key: "transport.dtls", label: "DTLS" },
  { key: "transport.sctp_open", label: "SCTP" },
  { key: "transport.stun", label: "STUN" },
  { key: "media.first_packet", label: "Media" },
];

const ConnectionTimeline = ({
  marks,
  status,
}: {
  marks: SetupMark[];
  status: CallStatus;
}) => {
  const [expanded, setExpanded] = useState(false);
  const t = useT();
  const byMark = new Map(marks.map((m) => [m.mark, m.elapsedMs]));
  const mediaMs = byMark.get("media.first_packet");

  if (status === "connected" && mediaMs !== undefined && !expanded) {
    return (
      <button
        type="button"
        onClick={() => setExpanded(true)}
        className="flex w-full items-center gap-2 rounded-md border border-border/60 px-2 py-1.5 text-xs text-muted-foreground"
      >
        <Check className="h-3.5 w-3.5 text-primary" />
        {t.calls.connectedIn} <span className="font-mono">{mediaMs} ms</span>
      </button>
    );
  }

  return (
    <div className="space-y-2 rounded-md border border-border/60 p-2">
      <div className="flex items-center justify-between">
        <span className="text-xs uppercase tracking-wide text-muted-foreground">
          {t.calls.connection}
        </span>
        {mediaMs !== undefined && (
          <button
            type="button"
            onClick={() => setExpanded(false)}
            className="font-mono text-xs text-muted-foreground"
          >
            {mediaMs} ms
          </button>
        )}
      </div>
      <div className="grid grid-cols-5 gap-1">
        {markSteps.map((step) => {
          const ms = byMark.get(step.key);
          const reached = ms !== undefined;
          return (
            <div key={step.key} className="flex flex-col items-center gap-1">
              <div
                className={`h-2.5 w-2.5 rounded-full ${reached ? "bg-primary" : "bg-muted-foreground/40"}`}
              />
              <span className="text-[10px] uppercase tracking-wide text-muted-foreground">
                {step.label}
              </span>
              <span
                className={`font-mono text-[10px] ${reached ? "text-foreground" : "text-muted-foreground"}`}
              >
                {reached ? `${ms}` : "—"}
              </span>
            </div>
          );
        })}
      </div>
    </div>
  );
};

const ReconnectingNotice = () => {
  const t = useT();
  const [showHint, setShowHint] = useState(false);
  return (
    <div className="space-y-1.5 rounded-md border border-amber-500/30 bg-amber-500/10 px-2 py-1.5 text-xs text-amber-600 dark:text-amber-400">
      <div className="flex items-center gap-2">
        <WifiOff className="h-3.5 w-3.5" />
        {t.calls.reconnectingMedia}
      </div>
      <button
        type="button"
        onClick={() => setShowHint((v) => !v)}
        className="underline underline-offset-2"
      >
        {t.calls.reconnectWhy}
      </button>
      {showHint && <p className="leading-relaxed">{t.calls.reconnectHint}</p>}
    </div>
  );
};

export const CallCard = ({ call }: { call: CallSummary }) => {
  const conn = useCalls((s) => s.ownConnections.get(call.callId));
  const quality = useCalls((s) => s.quality.get(call.callId));
  const marks = useCalls((s) => s.marks.get(call.callId));
  const peerVideoActive = useCalls((s) => s.peerVideoActive.get(call.callId));
  const outDeviceId = useDevices((s) => s.outId);
  const endCall = useEndCall();
  const t = useT();
  const [, force] = useState(0);
  const [micDb, setMicDb] = useState(-60);
  const [peerDb, setPeerDb] = useState(-60);
  const [cameraOn, setCameraOn] = useState(false);
  const audioRef = useRef<HTMLAudioElement>(null);
  const localVideoRef = useRef<HTMLVideoElement>(null);
  const remoteVideoRef = useRef<HTMLVideoElement>(null);
  // Show the video surface when either side has video: our camera is on, or the
  // peer has pushed video (either a call that started as video, or a mid-call
  // upgrade signalled via `peerVideoActive`).
  const hasVideo =
    !!conn?.localVideoStream || !!conn?.remoteVideoStream || !!peerVideoActive;

  const toggleCamera = async () => {
    if (!conn) return;
    if (conn.videoActive) {
      conn.stopVideo();
      setCameraOn(false);
      // Sinaliza o desligamento: sem isto o par fica com nosso último frame
      // congelado na tela, indistinguível de uma conexão travada.
      await disableVideo(call.sessionId, call.callId).catch(() => {});
    } else {
      // Signal the WhatsApp leg first so the peer accepts the upgrade, then open
      // the local camera and start pushing H.264 over the datachannel.
      await enableVideo(call.sessionId, call.callId).catch(() => {});
      const stream = await conn.startVideo();
      if (!stream) {
        toast.error("Não foi possível acessar a câmera do dispositivo.");
      }
      setCameraOn(stream !== null);
    }
  };

  useEffect(() => {
    const timer = setInterval(() => force((n) => n + 1), 1000);
    return () => clearInterval(timer);
  }, []);

  useEffect(() => {
    if (!conn) return;
    const offMic = attachMeter(conn.micStream, setMicDb);
    let offPeer: (() => void) | null = null;
    const wait = setInterval(() => {
      if (conn.remoteStream && audioRef.current) {
        audioRef.current.srcObject = conn.remoteStream;
        audioRef.current.play().catch(() => {});
        offPeer = attachMeter(conn.remoteStream, setPeerDb);
        clearInterval(wait);
      }
    }, 200);
    return () => {
      offMic();
      offPeer?.();
      clearInterval(wait);
    };
  }, [conn]);

  useEffect(() => {
    const el = audioRef.current as
      (HTMLAudioElement & { setSinkId?: (id: string) => Promise<void> }) | null;
    if (!el || !outDeviceId || typeof el.setSinkId !== "function") return;
    el.setSinkId(outDeviceId).catch(() => {});
  }, [outDeviceId, conn]);

  useEffect(() => {
    if (!conn) return;
    if (localVideoRef.current && conn.localVideoStream) {
      localVideoRef.current.srcObject = conn.localVideoStream;
      localVideoRef.current.play().catch(() => {});
    }
    if (remoteVideoRef.current && conn.remoteVideoStream) {
      remoteVideoRef.current.srcObject = conn.remoteVideoStream;
      remoteVideoRef.current.play().catch((err) => {
        if (err.name !== "AbortError") {
          console.warn("remote video play error", err);
        }
      });
    }
  }, [conn, cameraOn, peerVideoActive, hasVideo]);

  const isHeld = useCalls((s) => s.onHold.get(call.callId)) ?? false;
  const isRecording = useCalls((s) => s.recording.get(call.callId)) ?? false;
  const [showTransferInput, setShowTransferInput] = useState(false);
  const [transferOperator, setTransferOperator] = useState("");

  const toggleHold = async () => {
    if (isHeld) {
      await resumeCall(call.sessionId, call.callId);
    } else {
      await holdCall(call.sessionId, call.callId);
    }
  };

  const toggleRecord = async () => {
    await setRecording(call.sessionId, call.callId, !isRecording);
  };

  const handleTransferSubmit = async () => {
    if (!transferOperator.trim()) return;
    await transferCall(call.sessionId, call.callId, transferOperator.trim());
    setShowTransferInput(false);
  };

  return (
    <Card>
      <CardContent className="space-y-3 p-4">
        <div className="flex items-start justify-between gap-3">
          <div className="flex min-w-0 items-center gap-3">
            <PeerAvatar
              name={call.peerName || call.peer}
              photoUrl={call.peerPhotoUrl}
            />
            <div className="min-w-0">
              <p className="truncate font-medium">
                {call.peerName || call.peer}
              </p>
              <div className="mt-1 flex items-center gap-1.5">
                <StatusBadge
                  tone={callStatusTone(call.status)}
                  pulse={callStatusPulse(call.status)}
                >
                  {call.status === "connected"
                    ? formatCallDuration(call.startedAt)
                    : t.calls.status[call.status]}
                </StatusBadge>
                {isHeld && <StatusBadge tone="warn">{t.calls.onHold}</StatusBadge>}
                {isRecording && <StatusBadge tone="danger">🔴 {t.calls.recording}</StatusBadge>}
              </div>
            </div>
          </div>
          <div className="flex items-center gap-2">
            {conn && call.status === "connected" && (
              <>
                <Tooltip>
                  <TooltipTrigger asChild>
                    <Button
                      variant={isHeld ? "secondary" : "outline"}
                      size="icon"
                      onClick={() => void toggleHold()}
                      aria-label={isHeld ? t.calls.resume : t.calls.hold}
                    >
                      {isHeld ? <Play className="h-4 w-4 text-amber-500" /> : <Pause className="h-4 w-4" />}
                    </Button>
                  </TooltipTrigger>
                  <TooltipContent>{isHeld ? t.calls.resume : t.calls.hold}</TooltipContent>
                </Tooltip>

                <Tooltip>
                  <TooltipTrigger asChild>
                    <Button
                      variant={isRecording ? "destructive" : "outline"}
                      size="icon"
                      onClick={() => void toggleRecord()}
                      aria-label={isRecording ? t.calls.stopRecord : t.calls.record}
                    >
                      <Disc className={`h-4 w-4 ${isRecording ? "animate-pulse" : ""}`} />
                    </Button>
                  </TooltipTrigger>
                  <TooltipContent>{isRecording ? t.calls.stopRecord : t.calls.record}</TooltipContent>
                </Tooltip>

                <Tooltip>
                  <TooltipTrigger asChild>
                    <Button
                      variant="outline"
                      size="icon"
                      onClick={() => setShowTransferInput(!showTransferInput)}
                      aria-label={t.calls.transfer}
                    >
                      <Forward className="h-4 w-4" />
                    </Button>
                  </TooltipTrigger>
                  <TooltipContent>{t.calls.transfer}</TooltipContent>
                </Tooltip>

                <Tooltip>
                  <TooltipTrigger asChild>
                    <Button
                      variant={conn.videoActive ? "secondary" : "outline"}
                      size="icon"
                      onClick={() => void toggleCamera()}
                      aria-label={
                        conn.videoActive
                          ? t.calls.disableCamera
                          : t.calls.enableCamera
                      }
                    >
                      {conn.videoActive ? (
                        <VideoOff className="h-4 w-4" />
                      ) : (
                        <Video className="h-4 w-4" />
                      )}
                    </Button>
                  </TooltipTrigger>
                  <TooltipContent>
                    {conn.videoActive
                      ? t.calls.disableCamera
                      : t.calls.enableCamera}
                  </TooltipContent>
                </Tooltip>
              </>
            )}
            <Tooltip>
              <TooltipTrigger asChild>
                <Button
                  variant="destructive"
                  size="icon"
                  onClick={() =>
                    endCall.mutate({ sid: call.sessionId, callId: call.callId })
                  }
                  aria-label={t.calls.endCall}
                >
                  <PhoneOff className="h-4 w-4" />
                </Button>
              </TooltipTrigger>
              <TooltipContent>{t.calls.endCall}</TooltipContent>
            </Tooltip>
          </div>
        </div>

        {showTransferInput && (
          <div className="flex items-center gap-2 p-2 bg-muted/40 rounded-md">
            <input
              type="text"
              className="flex-1 text-xs px-2 py-1 bg-background border rounded"
              placeholder="ID do Operador"
              value={transferOperator}
              onChange={(e) => setTransferOperator(e.target.value)}
            />
            <Button size="sm" className="h-7 text-xs px-2" onClick={() => void handleTransferSubmit()}>
              Transferir
            </Button>
          </div>
        )}
        {call.status === "reconnecting" && <ReconnectingNotice />}
        {hasVideo && (
          <div className="relative overflow-hidden rounded-md bg-black">
            <video
              ref={remoteVideoRef}
              autoPlay
              playsInline
              className="aspect-video w-full bg-black object-cover"
            />
            <video
              ref={localVideoRef}
              autoPlay
              playsInline
              muted
              className="absolute bottom-2 right-2 aspect-video w-24 rounded border border-white/20 bg-black object-cover shadow-lg"
            />
          </div>
        )}
        {marks && marks.length > 0 && (
          <ConnectionTimeline marks={marks} status={call.status} />
        )}
        <Meter label={t.calls.mic} db={micDb} />
        <Meter label={t.calls.peer} db={peerDb} />
        {call.status === "connected" && <QualityPanel q={quality} />}
        <audio ref={audioRef} autoPlay />
      </CardContent>
    </Card>
  );
};
