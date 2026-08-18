import { useState } from "react";
import { Loader2 } from "lucide-react";
import { QRCodeSVG } from "qrcode.react";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Skeleton } from "@/components/ui/skeleton";
import { pairSessionByPhone } from "@/services/sessions";
import { useSessions } from "@/stores/sessions";
import { useT } from "@/hooks/useT";
import type { SessionInfo } from "@/types/session";

export const SessionPairing = ({ session }: { session: SessionInfo }) => {
  const qr = useSessions((s) => s.qrs[session.id]);
  const t = useT();
  const [byPhone, setByPhone] = useState(false);
  const [phone, setPhone] = useState("");
  const [requesting, setRequesting] = useState(false);
  const [failed, setFailed] = useState(false);
  // O código vem da resposta do POST e também pelo SSE (session.code). O estado local
  // cobre a janela entre os dois e o caso de o SSE estar caído.
  const [code, setCode] = useState<string | null>(null);
  const shownCode = session.code ?? code;

  const requestCode = async () => {
    setRequesting(true);
    setFailed(false);
    try {
      const res = await pairSessionByPhone(session.id, phone);
      setCode(res.code);
    } catch {
      setFailed(true);
    } finally {
      setRequesting(false);
    }
  };

  return (
    <div className="flex min-h-[55vh] items-center justify-center">
      <Card className="w-full max-w-md">
        <CardHeader className="items-center text-center">
          <CardTitle>{t.pairing.title(session.name)}</CardTitle>
          <CardDescription>
            {shownCode ? t.pairing.codeDescription : t.pairing.description}
          </CardDescription>
        </CardHeader>
        <CardContent className="flex flex-col items-center gap-4">
          {shownCode ? (
            <>
              <p className="text-sm text-muted-foreground">
                {t.pairing.codeTitle}
              </p>
              <div className="rounded-lg border px-6 py-4 font-mono text-3xl tracking-[0.35em]">
                {shownCode}
              </div>
            </>
          ) : byPhone ? (
            <div className="flex w-full flex-col gap-3">
              <div className="flex flex-col gap-1.5">
                <Label htmlFor="pair-phone">{t.pairing.phoneLabel}</Label>
                <Input
                  id="pair-phone"
                  inputMode="tel"
                  autoComplete="tel"
                  placeholder={t.pairing.phonePlaceholder}
                  value={phone}
                  onChange={(e) => setPhone(e.target.value)}
                />
              </div>
              {failed && (
                <p className="text-sm text-destructive">
                  {t.pairing.codeFailed}
                </p>
              )}
              <Button
                onClick={requestCode}
                disabled={requesting || phone.replace(/\D/g, "").length < 8}
              >
                {requesting && (
                  <Loader2 className="mr-2 h-4 w-4 animate-spin" />
                )}
                {requesting ? t.pairing.requestingCode : t.pairing.requestCode}
              </Button>
            </div>
          ) : qr ? (
            <div className="rounded-lg border bg-white p-3">
              <QRCodeSVG value={qr} size={232} marginSize={1} />
            </div>
          ) : session.state === "logged_out" ? (
            <Badge variant="destructive">{t.pairing.disconnectedBadge}</Badge>
          ) : (
            <>
              <Skeleton className="h-[258px] w-[258px] rounded-lg" />
              <Badge variant="muted" className="gap-1.5">
                <Loader2 className="h-3 w-3 animate-spin" />{" "}
                {t.pairing.waitingQr}
              </Badge>
            </>
          )}
          {!shownCode && (
            <Button
              variant="link"
              size="sm"
              onClick={() => {
                setByPhone((v) => !v);
                setFailed(false);
              }}
            >
              {byPhone ? t.pairing.useQr : t.pairing.usePhoneCode}
            </Button>
          )}
        </CardContent>
      </Card>
    </div>
  );
};
