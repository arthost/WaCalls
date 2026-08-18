import { useState } from "react";
import { Download, History, Mic } from "lucide-react";
import { toast } from "sonner";
import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
  SheetTrigger,
} from "@/components/ui/sheet";
import { Button } from "@/components/ui/button";
import { ScrollArea } from "@/components/ui/scroll-area";
import { Separator } from "@/components/ui/separator";
import { EmptyState } from "@/components/shared/EmptyState";
import { PeerAvatar } from "@/components/domain/contacts/PeerAvatar";
import { useHistory } from "@/hooks/useHistory";
import { useRecordings } from "@/hooks/useRecordings";
import { exportHistoryCsv } from "@/services/history";
import { downloadRecording } from "@/services/recordings";
import { useT } from "@/hooks/useT";
import { useLocale } from "@/stores/locale";

export const HistoryDrawer = ({ sid }: { sid: string }) => {
  const [open, setOpen] = useState(false);
  const [exporting, setExporting] = useState(false);
  const t = useT();
  const locale = useLocale((s) => s.locale);
  const { data, fetchNextPage, hasNextPage, isFetchingNextPage } = useHistory(
    sid,
    open,
  );
  const { byCallId: recordings } = useRecordings(open);
  const rows = data?.pages.flatMap((p) => p.calls) ?? [];

  const onExport = async () => {
    setExporting(true);
    try {
      await exportHistoryCsv(sid);
    } catch (e) {
      toast.error(e instanceof Error ? e.message : String(e));
    } finally {
      setExporting(false);
    }
  };

  const onDownloadRecording = async (callId: string) => {
    try {
      await downloadRecording(callId);
    } catch (e) {
      toast.error(e instanceof Error ? e.message : String(e));
    }
  };

  return (
    <Sheet open={open} onOpenChange={setOpen}>
      <SheetTrigger asChild>
        <Button variant="outline" size="sm">
          <History className="h-4 w-4" />
          {t.history.button}
        </Button>
      </SheetTrigger>
      <SheetContent side="right" className="w-full p-0 sm:max-w-md">
        <SheetHeader className="p-6 pb-4">
          <SheetTitle>{t.history.title}</SheetTitle>
        </SheetHeader>
        <Separator />
        <div className="flex justify-end px-6 pt-3">
          <Button
            variant="outline"
            size="sm"
            disabled={exporting || rows.length === 0}
            onClick={() => void onExport()}
          >
            <Download className="h-4 w-4" />
            {t.history.exportCsv}
          </Button>
        </div>
        <ScrollArea className="h-[calc(100vh-9rem)] px-6 py-4">
          {rows.length === 0 ? (
            <EmptyState
              title={t.history.emptyTitle}
              description={t.history.emptyDescription}
            />
          ) : (
            <>
              <ul className="space-y-2">
                {rows.map((r) => {
                  const rec = recordings.get(r.callId);
                  return (
                    <li
                      key={r.callId}
                      className="flex items-center gap-3 rounded-lg border p-3"
                    >
                      <PeerAvatar
                        name={r.peerName || r.peer}
                        photoUrl={r.peerPhotoUrl}
                      />
                      <div className="min-w-0 flex-1">
                        <p className="truncate font-medium">
                          {r.peerName || r.peer}
                        </p>
                        <p className="text-xs text-muted-foreground">
                          {t.calls.direction[r.direction]} ·{" "}
                          <span className="font-mono">
                            {new Date(r.startedAt).toLocaleString(locale)}
                          </span>
                        </p>
                      </div>
                      {/* A recording that is still open has a zero length in its WAV
                          header, so offering it would hand out a file that plays as
                          empty — show it as pending instead. */}
                      {rec && (
                        <Button
                          variant="ghost"
                          size="icon"
                          disabled={rec.active}
                          title={
                            rec.active
                              ? t.history.recordingPending
                              : t.history.downloadRecording
                          }
                          aria-label={
                            rec.active
                              ? t.history.recordingPending
                              : t.history.downloadRecording
                          }
                          onClick={() => void onDownloadRecording(r.callId)}
                        >
                          <Mic className="h-4 w-4" />
                        </Button>
                      )}
                    </li>
                  );
                })}
              </ul>
              {hasNextPage && (
                <Button
                  variant="ghost"
                  size="sm"
                  className="mt-3 w-full"
                  disabled={isFetchingNextPage}
                  onClick={() => void fetchNextPage()}
                >
                  {isFetchingNextPage ? t.history.loading : t.history.loadMore}
                </Button>
              )}
            </>
          )}
        </ScrollArea>
      </SheetContent>
    </Sheet>
  );
};
