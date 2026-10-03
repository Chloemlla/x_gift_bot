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

export async function request<T>(
  path: string,
  body?: unknown,
  signal?: AbortSignal,
): Promise<{ ok: boolean; data: T }> {
  const response = await fetch(path, {
    method: body === undefined ? "GET" : "POST",
    headers:
      body === undefined ? undefined : { "Content-Type": "application/json" },
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
  return { ok: response.ok, data };
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
    if (this.state.failed)
      return (
        <Container sx={{ py: 8 }}>
          <Alert severity="error">
            页面暂时无法显示。若已提交兑换，请保留兑换码，重新打开页面后查询原订单；不要重复兑换。
          </Alert>
          <Button href="/" sx={{ mt: 2 }}>
            重新打开兑换页
          </Button>
        </Container>
      );
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
        <ErrorBoundary>{node}</ErrorBoundary>
      </ThemeProvider>
    </CacheProvider>,
  );
}

export function Shell({
  admin = false,
  children,
}: {
  admin?: boolean;
  children: ReactNode;
}) {
  return (
    <Container
      id="main"
      component="main"
      maxWidth={admin ? "lg" : "sm"}
      sx={{ py: { xs: 3, sm: 6 } }}
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
  );
}
