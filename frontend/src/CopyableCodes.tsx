import { useEffect, useState } from "react";
import { Alert, Box, Button, Stack, Typography } from "@mui/material";
import ContentCopyRounded from "@mui/icons-material/ContentCopyRounded";
import CheckRounded from "@mui/icons-material/CheckRounded";

export function CopyableCodes({ codes }: { codes: string[] }) {
  const [copied, setCopied] = useState<number | "all" | null>(null);
  const [error, setError] = useState(false);
  // The ✓ marks a recent copy; clear it so a later visit does not read as fresh.
  useEffect(() => {
    if (copied === null) return;
    const timer = setTimeout(() => setCopied(null), 5000);
    return () => clearTimeout(timer);
  }, [copied]);
  async function copy(index: number | "all") {
    try {
      await navigator.clipboard.writeText(
        index === "all" ? codes.join("\n") : codes[index],
      );
      setCopied(index);
      setError(false);
    } catch {
      setCopied(null);
      setError(true);
    }
  }
  return (
    <Box sx={{ my: 2 }}>
      <Stack
        direction="row"
        alignItems="center"
        justifyContent="space-between"
        spacing={1}
        sx={{ mb: 1 }}
      >
        <Typography variant="body2" color="text.secondary">
          点击单枚兑换码即可复制
        </Typography>
        <Button
          size="small"
          color="secondary"
          startIcon={<ContentCopyRounded />}
          onClick={() => void copy("all")}
          sx={{ px: 1.5, flexShrink: 0 }}
        >
          复制全部
        </Button>
      </Stack>
      <Box
        component="ul"
        aria-label="新生成的兑换码"
        tabIndex={0}
        sx={{
          m: 0,
          p: 0,
          listStyle: "none",
          maxHeight: 300,
          overflowY: "auto",
          border: 1,
          borderColor: "divider",
          borderRadius: 2,
        }}
      >
        {codes.map((code, index) => (
          <Box component="li" key={code}>
            <Button
              fullWidth
              aria-label={`复制第 ${index + 1} 枚兑换码`}
              onClick={() => void copy(index)}
              endIcon={
                copied === index ? <CheckRounded /> : <ContentCopyRounded />
              }
              sx={{
                py: 1.5,
                px: 2,
                borderRadius: 0,
                justifyContent: "space-between",
                textAlign: "left",
                borderBottom: index < codes.length - 1 ? 1 : 0,
                borderColor: "divider",
                color: "text.primary",
                gap: 1,
              }}
            >
              <Box
                component="code"
                sx={{
                  fontFamily: "monospace",
                  fontSize: ".8125rem",
                  overflowWrap: "anywhere",
                  userSelect: "text",
                }}
              >
                {code}
              </Box>
            </Button>
          </Box>
        ))}
      </Box>
      {copied !== null && (
        <Typography
          role="status"
          variant="body2"
          color="success.main"
          sx={{ mt: 1 }}
        >
          {copied === "all"
            ? `已复制全部 ${codes.length} 枚兑换码。`
            : `已复制第 ${copied + 1} 枚兑换码。`}
        </Typography>
      )}
      {error && (
        <Alert severity="warning" sx={{ mt: 1 }}>
          浏览器未允许访问剪贴板。请选中兑换码手动复制，或使用下方「下载 TXT」。
        </Alert>
      )}
    </Box>
  );
}
