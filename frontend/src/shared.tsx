import { Component, type ReactNode } from "react";
import { createRoot } from "react-dom/client";
import { CacheProvider } from "@emotion/react";
import createCache from "@emotion/cache";
import {
  Alert,
  Box,
  Button,
  Container,
  CssBaseline,
  Link,
  ThemeProvider,
  Typography,
} from "@mui/material";
import GitHubIcon from "@mui/icons-material/GitHub";
import { theme } from "./theme";
import { humanToken, HumanVerification } from "./HumanVerification";

export async function request<T>(
  path: string,
  body?: unknown,
  signal?: AbortSignal,
): Promise<{ ok: boolean; data: T; status?: number }> {
  const actions: Record<string, string> = { "/api/redeem": "redeem", "/api/check": "check", "/api/manual-link": "manual_link" };
  let token = "";
  if (body !== undefined && actions[path]) {
    try {
      token = await humanToken(actions[path], signal);
      signal?.throwIfAborted();
    } catch (error) {
      return { ok: false, data: { status: "verification_required", message: error instanceof Error && error.name !== "TimeoutError" && error.name !== "AbortError" ? error.message : "人机验证已超时，本次操作尚未提交，请重试。" } as T };
    }
  }
  const response = await fetch(path, {
    method: body === undefined ? "GET" : "POST",
    headers:
      body === undefined ? undefined : { "Content-Type": "application/json", ...(token ? { "X-Turnstile-Token": token } : {}) },
    body: body === undefined ? undefined : JSON.stringify(body),
    cache: "no-store",
    signal: signal ?? AbortSignal.timeout(45000),
  });
  let data: T;
  try {
    data = await response.json();
  } catch {
    throw new Error("服务暂时不可用，请稍后查询。");
  }
  return { ok: response.ok, data, status: response.status };
}

class ErrorBoundary extends Component<
  { children: ReactNode },
  { failed: boolean }
> {
  state = { failed: false };
  static getDerivedStateFromError() {
    return { failed: true };
  }
  render() {
    if (this.state.failed) {
      const admin = window.location.pathname.startsWith("/admin");
      return (
        <Container sx={{ py: 8 }}>
          <Alert severity="error">
            {admin
              ? "管理页暂时无法显示。进行中的兑换不受影响，请重新打开后继续核实。"
              : "页面暂时无法显示。若已提交兑换，请保留兑换码，重新打开页面后查询原订单；不要重复兑换。"}
          </Alert>
          <Button href={admin ? "/admin" : "/"} sx={{ mt: 2 }}>
            {admin ? "重新打开管理页" : "重新打开兑换页"}
          </Button>
        </Container>
      );
    }
    return this.props.children;
  }
}

export function mount(node: ReactNode) {
  const cache = createCache({
    key: "xgift",
    nonce: document.querySelector<HTMLMetaElement>('meta[name="csp-nonce"]')
      ?.content,
    prepend: true,
  });
  createRoot(document.getElementById("root")!).render(
    <CacheProvider value={cache}>
      <ThemeProvider
        theme={theme}
        defaultMode="system"
        modeStorageKey="xgift-mode"
      >
        <CssBaseline />
        <ErrorBoundary>{node}<HumanVerification /></ErrorBoundary>
      </ThemeProvider>
    </CacheProvider>,
  );
}

export function Shell({
  admin = false,
  maxWidth,
  children,
}: {
  admin?: boolean;
  maxWidth?: "xs" | "sm" | "md" | "lg";
  children: ReactNode;
}) {
  return (
    <>
      <Link
        href="#main"
        sx={{
          position: "absolute",
          left: 16,
          top: -64,
          zIndex: (theme) => theme.zIndex.tooltip,
          px: 2,
          py: 1,
          borderRadius: 1,
          bgcolor: "background.paper",
          color: "text.primary",
          fontWeight: 600,
          transition: "top 160ms ease",
          "&:focus-visible": { top: 16 },
          "@media (prefers-reduced-motion: reduce)": { transition: "none" },
        }}
      >
        跳转到主要内容
      </Link>
      <Container
        id="main"
        component="main"
        tabIndex={-1}
        maxWidth={maxWidth ?? (admin ? "lg" : "sm")}
        sx={{
          py: { xs: 3, sm: 6 },
          "&:focus-visible": { outline: "none" },
        }}
      >
      {children}
      <Box
        component="footer"
        sx={{
          mt: { xs: 4, sm: 6 },
          pt: 2,
          borderTop: 1,
          borderColor: "divider",
          display: "flex",
          justifyContent: "center",
          alignItems: "center",
          flexWrap: "wrap",
          gap: 0.75,
          color: "text.secondary",
        }}
      >
        <Typography variant="body2" component="span">
          © 2026 mizorewww
        </Typography>
        <Typography variant="body2" component="span" aria-hidden="true">
          ·
        </Typography>
        <Link
          variant="body2"
          color="inherit"
          underline="hover"
          href="https://github.com/mizorewww/x_gift_bot/blob/main/LICENSE"
          target="_blank"
          rel="noopener noreferrer"
        >
          MIT License
        </Link>
        <Typography variant="body2" component="span" aria-hidden="true">
          ·
        </Typography>
        <Link
          variant="body2"
          color="inherit"
          underline="hover"
          href="https://github.com/mizorewww/x_gift_bot"
          target="_blank"
          rel="noopener noreferrer"
          sx={{ display: "inline-flex", alignItems: "center", gap: 0.5 }}
        >
          <GitHubIcon sx={{ fontSize: 16 }} aria-hidden="true" />
          GitHub
        </Link>
      </Box>
      </Container>
    </>
  );
}
