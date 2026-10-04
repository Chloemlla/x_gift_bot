import { useEffect, useRef, useState, useSyncExternalStore } from "react";
import { Alert, Box, Button, CircularProgress, Dialog, DialogActions, DialogContent, DialogTitle, Stack, Typography } from "@mui/material";

type Turnstile = {
  render(container: HTMLElement, options: Record<string, unknown>): string;
  remove(id: string): void;
};
declare global { interface Window { turnstile?: Turnstile } }
type Challenge = { siteKey: string; action: string; finish: (token: string) => void };
let current: Challenge | null = null;
const listeners = new Set<() => void>();
const publish = () => listeners.forEach((f) => f());
const subscribe = (f: () => void) => { listeners.add(f); return () => { listeners.delete(f); }; };
let scriptPromise: Promise<Turnstile> | undefined;

function loadScript(): Promise<Turnstile> {
  if (window.turnstile) return Promise.resolve(window.turnstile);
  if (scriptPromise) return scriptPromise;
  scriptPromise = new Promise<Turnstile>((resolve, reject) => {
    const script = document.createElement("script");
    script.src = "https://challenges.cloudflare.com/turnstile/v0/api.js?render=explicit";
    script.async = true;
    script.nonce = document.querySelector<HTMLMetaElement>('meta[name="csp-nonce"]')?.content || "";
    const timer = setTimeout(() => fail(), 15000);
    function fail() {
      clearTimeout(timer); script.remove(); scriptPromise = undefined;
      reject(new Error("验证组件加载失败，请检查网络后重试。"));
    }
    script.onerror = fail;
    script.onload = () => {
      clearTimeout(timer);
      if (window.turnstile) resolve(window.turnstile); else fail();
    };
    document.head.appendChild(script);
  });
  return scriptPromise;
}

export async function humanToken(action: string, signal?: AbortSignal): Promise<string> {
  const response = await fetch("/api/security", { cache: "no-store", signal: signal ?? AbortSignal.timeout(10000) });
  if (!response.ok) throw new Error("暂时无法加载人机验证，请稍后重试。");
  const config = await response.json() as { turnstile_enabled: boolean; turnstile_site_key: string };
  if (config.turnstile_enabled === false) return "";
  if (config.turnstile_enabled !== true || !config.turnstile_site_key) throw new Error("人机验证配置不可用，请稍后重试。");
  if (current) throw new Error("请先完成正在进行的人机验证。");
  signal?.throwIfAborted();
  return new Promise<string>((resolve, reject) => {
    let finished = false;
    const cancel = () => finish("");
    const timer = setTimeout(cancel, 120000);
    function finish(token: string) {
      if (finished) return;
      finished = true; clearTimeout(timer); signal?.removeEventListener("abort", cancel);
      current = null; publish();
      if (token) resolve(token);
      else reject(new Error("人机验证未完成，本次操作尚未提交，请重试。"));
    }
    current = { siteKey: config.turnstile_site_key, action, finish };
    signal?.addEventListener("abort", cancel, { once: true });
    publish();
  });
}

function Widget({ challenge }: { challenge: Challenge }) {
  const root = useRef<HTMLDivElement>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    let active = true;
    let widget: string | undefined;
    let api: Turnstile | undefined;
    setLoading(true); setError("");
    void loadScript().then((loaded) => {
      if (!active || !root.current) return;
      api = loaded;
      widget = api.render(root.current, {
        sitekey: challenge.siteKey, action: challenge.action, theme: "auto",
        size: window.innerWidth < 380 ? "compact" : "flexible",
        "response-field": false,
        callback: (token: string) => { if (active) challenge.finish(token); },
        "error-callback": () => { if (active) { setLoading(false); setError("验证未能完成，请重试，或检查浏览器是否拦截了验证组件。"); } },
        "expired-callback": () => { if (active) setError("验证已过期，请重新验证。"); },
        "timeout-callback": () => { if (active) setError("验证超时，请重新验证。"); },
      });
      setLoading(false);
    }).catch((e: Error) => { if (active) { setLoading(false); setError(e.message); } });
    return () => { active = false; if (widget && api) api.remove(widget); };
  }, [challenge, attempt]);
  return <Stack spacing={2}>
    <Typography variant="body2" color="text.secondary">请完成 Cloudflare 安全验证，通过后会继续刚才的操作。</Typography>
    {loading && <Stack direction="row" spacing={1} alignItems="center" role="status"><CircularProgress size={18} /><Typography variant="body2">正在加载验证…</Typography></Stack>}
    <Box ref={root} sx={{ minHeight: 65, minWidth: 0 }} />
    {error && <Alert severity="warning" sx={{ alignItems: "center", "& .MuiAlert-action": { flexShrink: 0 } }} action={<Button color="inherit" sx={{ whiteSpace: "nowrap", minWidth: 56 }} onClick={() => setAttempt((n) => n + 1)}>重试</Button>}>{error}</Alert>}
  </Stack>;
}

export function HumanVerification() {
  const challenge = useSyncExternalStore(subscribe, () => current, () => null);
  return <Dialog open={Boolean(challenge)} onClose={() => challenge?.finish("")} maxWidth="xs" fullWidth aria-labelledby="human-verification-title" sx={{ "& .MuiDialog-paper": { m: 2, width: "calc(100% - 32px)" } }}>
    <DialogTitle id="human-verification-title">人机验证</DialogTitle>
    <DialogContent sx={{ px: 2 }}>{challenge && <Widget challenge={challenge} />}</DialogContent>
    <DialogActions><Button onClick={() => challenge?.finish("")}>取消</Button></DialogActions>
  </Dialog>;
}
