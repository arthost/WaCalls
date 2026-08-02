import { create } from "zustand";
import { eventStream, type BrokerEvent } from "@/lib/event-stream";
import { getClientId } from "@/lib/client-id";
import { queryClient, queryKeys } from "@/lib/query";
import type { OpenCall } from "@/lib/webrtc";
import type {
  CallSummary,
  IncomingPayload,
  QualitySample,
  SetupMark,
} from "@/types/call";

type State = {
  calls: CallSummary[];
  ownConnections: Map<string, OpenCall>;
  incoming: IncomingPayload | null;
  quality: Map<string, QualitySample>;
  marks: Map<string, SetupMark[]>;
  // Whether the peer is currently sending video, keyed by call id. Driven by the
  // mid-call `call-video-state` broker event (1 = Enabled, 0 = Disabled).
  peerVideoActive: Map<string, boolean>;
  onHold: Map<string, boolean>;
  recording: Map<string, boolean>;
  pendingTransfer: { sessionId: string; callId: string; fromOwner: string } | null;
};

export const useCalls = create<State>(() => ({
  calls: [],
  ownConnections: new Map(),
  incoming: null,
  quality: new Map(),
  marks: new Map(),
  peerVideoActive: new Map(),
  onHold: new Map(),
  recording: new Map(),
  pendingTransfer: null,
}));

let wired = false;
export const ensureCallsWired = (): void => {
  if (wired) return;
  wired = true;
  eventStream.on((ev: BrokerEvent) => {
    if (ev.type === "call-list") {
      useCalls.setState((s) => {
        // call-list is the authoritative set of live calls; prune quality samples for calls no
        // longer present (e.g. a call ended while we were disconnected and missed its call-ended).
        const ids = new Set(ev.calls.map((c) => c.callId));
        const quality = new Map([...s.quality].filter(([id]) => ids.has(id)));
        const marks = new Map([...s.marks].filter(([id]) => ids.has(id)));
        const peerVideoActive = new Map(
          [...s.peerVideoActive].filter(([id]) => ids.has(id)),
        );
        return { calls: ev.calls, quality, marks, peerVideoActive };
      });
    } else if (ev.type === "call-status") {
      useCalls.setState((s) => ({
        calls: s.calls.map((c) =>
          c.callId === ev.id
            ? {
                ...c,
                sessionId: ev.sessionId,
                status: ev.status,
                peer: ev.peer,
                peerName: ev.peerName ?? c.peerName,
                peerPhotoUrl: ev.peerPhotoUrl ?? c.peerPhotoUrl,
                startedAt: ev.startedAt,
              }
            : c,
        ),
      }));
    } else if (ev.type === "call-quality") {
      useCalls.setState((s) => {
        // Ignore a straggler sample that raced past call-ended: only track quality for a live call,
        // otherwise the entry would never be pruned.
        if (!s.calls.some((c) => c.callId === ev.id)) return s;
        const next = new Map(s.quality);
        next.set(ev.id, {
          rttMs: ev.rttMs,
          jitterMs: ev.jitterMs,
          lossFraction: ev.lossFraction,
          hasRtt: ev.hasRtt,
        });
        return { quality: next };
      });
    } else if (ev.type === "call-mark") {
      useCalls.setState((s) => {
        if (!s.calls.some((c) => c.callId === ev.id)) return s;
        const existing = s.marks.get(ev.id) ?? [];
        if (existing.some((m) => m.mark === ev.mark)) return s; // keep the first of each phase
        const next = new Map(s.marks);
        next.set(ev.id, [
          ...existing,
          { mark: ev.mark, elapsedMs: ev.elapsedMs },
        ]);
        return { marks: next };
      });
    } else if (ev.type === "call-video-state") {
      useCalls.setState((s) => {
        if (!s.calls.some((c) => c.callId === ev.id)) return s;
        const active = ev.state === 1;
        const next = new Map(s.peerVideoActive);
        next.set(ev.id, active);
        return { peerVideoActive: next };
      });
    } else if (ev.type === "call-hold-state") {
      useCalls.setState((s) => {
        const next = new Map(s.onHold);
        next.set(ev.id, ev.onHold);
        return { onHold: next };
      });
    } else if (ev.type === "call-record-state") {
      useCalls.setState((s) => {
        const next = new Map(s.recording);
        next.set(ev.id, ev.recording);
        return { recording: next };
      });
    } else if (ev.type === "call-transfer") {
      if (ev.toOwner === getClientId()) {
        useCalls.setState({
          pendingTransfer: {
            sessionId: ev.sessionId,
            callId: ev.id,
            fromOwner: ev.fromOwner,
          },
        });
      }
    } else if (ev.type === "call-ended") {
      useCalls.setState((s) => {
        const conn = s.ownConnections.get(ev.id);
        if (conn) conn.close();
        const next = new Map(s.ownConnections);
        next.delete(ev.id);
        const nextQuality = new Map(s.quality);
        nextQuality.delete(ev.id);
        const nextMarks = new Map(s.marks);
        nextMarks.delete(ev.id);
        const nextPeerVideo = new Map(s.peerVideoActive);
        nextPeerVideo.delete(ev.id);
        const nextHold = new Map(s.onHold);
        nextHold.delete(ev.id);
        const nextRec = new Map(s.recording);
        nextRec.delete(ev.id);
        return {
          calls: s.calls.filter((c) => c.callId !== ev.id),
          ownConnections: next,
          quality: nextQuality,
          marks: nextMarks,
          peerVideoActive: nextPeerVideo,
          onHold: nextHold,
          recording: nextRec,
          incoming: s.incoming?.callId === ev.id ? null : s.incoming,
          pendingTransfer: s.pendingTransfer?.callId === ev.id ? null : s.pendingTransfer,
        };
      });
      void queryClient.invalidateQueries({ queryKey: queryKeys.history });
    } else if (ev.type === "incoming") {
      useCalls.setState({
        incoming: {
          sessionId: ev.sessionId,
          callId: ev.id,
          peer: ev.peer,
          peerName: ev.peerName,
          peerPhotoUrl: ev.peerPhotoUrl,
          offeredAt: ev.offeredAt,
          isVideo: ev.isVideo,
        },
      });
    } else if (ev.type === "incoming-claimed") {
      useCalls.setState((s) =>
        s.incoming?.callId === ev.id ? { incoming: null } : s,
      );
    }
  });
};

export const isMine = (call: CallSummary): boolean =>
  call.owner === getClientId();

export const registerOwnConnection = (id: string, conn: OpenCall): void => {
  useCalls.setState((s) => {
    const next = new Map(s.ownConnections);
    next.set(id, conn);
    return { ownConnections: next };
  });
};

export const clearIncoming = (): void => useCalls.setState({ incoming: null });

export const clearPendingTransfer = (): void =>
  useCalls.setState({ pendingTransfer: null });
