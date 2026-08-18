import { useQuery } from "@tanstack/react-query";
import { fetchRecordings } from "@/services/recordings";
import type { Recording } from "@/types/recording";

// Returns the recordings keyed by call ID, so a history row can look itself up in
// O(1) instead of scanning the list per render.
export const useRecordings = (enabled: boolean) => {
  const { data, ...rest } = useQuery({
    queryKey: ["recordings"],
    queryFn: fetchRecordings,
    enabled,
  });
  const byCallId = new Map<string, Recording>(
    (data?.recordings ?? []).map((r) => [r.callId, r]),
  );
  return { byCallId, ...rest };
};
