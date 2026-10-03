import { useCallback, useEffect, useRef, useState } from "react";
import {
  Alert,
  Box,
  Button,
  Collapse,
  Paper,
  Skeleton,
  Stack,
  Typography,
  useMediaQuery,
  useTheme,
  type CssVarsTheme,
} from "@mui/material";
import ExpandMoreRounded from "@mui/icons-material/ExpandMoreRounded";
import QueryStatsRounded from "@mui/icons-material/QueryStatsRounded";
import { stats as fetchStats, type AdminStatsDetail } from "./adminApi";

const visuallyHidden = {
  position: "absolute",
  width: 1,
  height: 1,
  p: 0,
  m: -1,
  overflow: "hidden",
  clip: "rect(0 0 0 0)",
  whiteSpace: "nowrap",
  border: 0,
} as const;

function StatCard({
  label,
  value,
  hint,
}: {
  label: string;
  value: string;
  hint?: string;
}) {
  return (
    <Box
      sx={{ border: 1, borderColor: "divider", borderRadius: 2, p: 1.5, minWidth: 0 }}
    >
      <Typography variant="caption" color="text.secondary" component="p">
        {label}
      </Typography>
      <Typography variant="h3" component="p" sx={{ mt: 0.25 }}>
        {value}
      </Typography>
      {hint && (
        <Typography variant="caption" color="text.secondary" component="p">
          {hint}
        </Typography>
      )}
    </Box>
  );
}

const series = [
  { key: "created", label: "生成", fill: "primary.main" },
  { key: "redeemed", label: "兑换", fill: "secondary.main" },
  { key: "succeeded", label: "成功", fill: "success.main" },
] as const;

function ActivityChart({ daily }: { daily: AdminStatsDetail["daily"] }) {
  const theme = useTheme<CssVarsTheme>();
  // sx does not map SVG fill/stroke to the palette; use theme vars directly.
  const fills = {
    created: theme.vars.palette.primary.main,
    redeemed: theme.vars.palette.secondary.main,
    succeeded: theme.vars.palette.success.main,
  } as const;
  const ink = theme.vars.palette.text.secondary;
  const rule = theme.vars.palette.divider;
  const peak = Math.max(
    1,
    ...daily.flatMap((day) => [day.created, day.redeemed, day.succeeded]),
  );
  const totals = daily.reduce(
    (sum, day) => ({
      created: sum.created + day.created,
      redeemed: sum.redeemed + day.redeemed,
      succeeded: sum.succeeded + day.succeeded,
    }),
    { created: 0, redeemed: 0, succeeded: 0 },
  );
  const width = 720;
  const baseline = 128;
  const group = width / daily.length;
  const ticks = daily
    .map((day, index) => ({ day, index }))
    .filter(({ index }) => index % 5 === 0 || index === daily.length - 1);
  return (
    <Box>
      <Stack
        component="ul"
        direction="row"
        useFlexGap
        flexWrap="wrap"
        gap={2}
        sx={{ listStyle: "none", m: 0, p: 0, mb: 1 }}
      >
        {series.map((item) => (
          <Stack
            component="li"
            key={item.key}
            direction="row"
            alignItems="center"
            spacing={0.75}
          >
            <Box
              aria-hidden
              sx={{
                width: 10,
                height: 10,
                borderRadius: "50%",
                bgcolor: item.fill,
              }}
            />
            <Typography variant="caption" color="text.secondary">
              {item.label}
            </Typography>
          </Stack>
        ))}
      </Stack>
      <Box
        component="svg"
        role="img"
        aria-label={`最近 30 天共生成 ${totals.created} 枚，兑换 ${totals.redeemed} 枚，成功 ${totals.succeeded} 枚。`}
        viewBox={`0 0 ${width} 150`}
        preserveAspectRatio="none"
        sx={{ display: "block", width: "100%", height: 160 }}
      >
        <Box
          component="line"
          x1={0}
          x2={width}
          y1={baseline / 2}
          y2={baseline / 2}
          stroke={rule}
          strokeWidth={1}
          strokeDasharray="3 4"
        />
        <Box
          component="line"
          x1={0}
          x2={width}
          y1={baseline}
          y2={baseline}
          stroke={rule}
          strokeWidth={1}
        />
        {daily.map((day, index) =>
          series.map((item, order) => {
            const height =
              day[item.key] > 0
                ? Math.max(2, (day[item.key] / peak) * (baseline - 8))
                : 0;
            return (
              <Box
                component="rect"
                key={`${day.date}-${item.key}`}
                x={index * group + group * 0.18 + order * group * 0.24}
                y={baseline - height}
                width={group * 0.2}
                height={height}
                rx={1}
                fill={fills[item.key]}
              />
            );
          }),
        )}
        {ticks.map(({ day, index }) => (
          <Box
            component="text"
            key={day.date}
            x={
              index === 0
                ? 0
                : index === daily.length - 1
                  ? width
                  : index * group + group / 2
            }
            y={144}
            textAnchor={
              index === 0
                ? "start"
                : index === daily.length - 1
                  ? "end"
                  : "middle"
            }
            fill={ink}
            fontSize={11}
          >
            {day.date.slice(5).replace("-", "/")}
          </Box>
        ))}
      </Box>
      <Box component="table" sx={visuallyHidden}>
        <caption>最近 30 天每日生成、兑换和成功的兑换码数量</caption>
        <thead>
          <tr>
            <th scope="col">日期</th>
            <th scope="col">生成</th>
            <th scope="col">兑换</th>
            <th scope="col">成功</th>
          </tr>
        </thead>
        <tbody>
          {daily.map((day) => (
            <tr key={day.date}>
              <th scope="row">{day.date}</th>
              <td>{day.created}</td>
              <td>{day.redeemed}</td>
              <td>{day.succeeded}</td>
            </tr>
          ))}
        </tbody>
      </Box>
    </Box>
  );
}

export function StatsPanel({ refreshSignal }: { refreshSignal: number }) {
  const [expanded, setExpanded] = useState(true);
  const [data, setData] = useState<AdminStatsDetail | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const sequence = useRef(0);
  const reducedMotion = useMediaQuery("(prefers-reduced-motion: reduce)");
  const load = useCallback(async () => {
    const current = ++sequence.current;
    setLoading(true);
    setError("");
    try {
      const detail = await fetchStats();
      if (current === sequence.current) setData(detail);
    } catch (reason) {
      if (current === sequence.current) setError((reason as Error).message);
    } finally {
      if (current === sequence.current) setLoading(false);
    }
  }, []);
  useEffect(() => {
    void load();
  }, [load, refreshSignal]);

  const redeemedRate =
    data && data.codes.redeemed > 0
      ? `${Math.round(data.rates.redeemed * 100)}%`
      : "—";
  const successRate =
    data && data.codes.redeemed > 0
      ? `${Math.round(data.rates.success * 100)}%`
      : "—";
  const monthPeak = Math.max(1, ...(data?.months ?? []).map((m) => m.total));
  const stagePeak = Math.max(
    1,
    ...(data?.review_stages ?? []).map((stage) => stage.count),
  );
  return (
    <Paper variant="outlined" sx={{ mb: 3, overflow: "hidden" }}>
      <Button
        fullWidth
        aria-expanded={expanded}
        aria-controls="stats-panel-content"
        onClick={() => setExpanded((value) => !value)}
        sx={{
          justifyContent: "flex-start",
          gap: 1,
          px: { xs: 2.5, sm: 3 },
          py: 1.5,
          borderRadius: 0,
          color: "text.primary",
        }}
      >
        <QueryStatsRounded color="primary" />
        <Typography variant="h2" component="span" sx={{ flexGrow: 1, textAlign: "left" }}>
          统计概览
        </Typography>
        {data && (
          <Typography variant="body2" color="text.secondary">
            共 {data.codes.total} 枚
          </Typography>
        )}
        <ExpandMoreRounded
          sx={(theme) => ({
            transform: expanded ? "rotate(180deg)" : "none",
            transition: theme.transitions.create("transform"),
            "@media (prefers-reduced-motion: reduce)": { transition: "none" },
          })}
        />
      </Button>
      <Collapse in={expanded} timeout={reducedMotion ? 0 : undefined}>
        <Box
          id="stats-panel-content"
          role="region"
          aria-label="统计概览"
          sx={{ px: { xs: 2.5, sm: 3 }, pb: 3, pt: 1 }}
        >
          {loading && (
            <Stack spacing={2} aria-label="正在加载统计数据">
              <Box
                sx={{
                  display: "grid",
                  gridTemplateColumns: {
                    xs: "repeat(2, 1fr)",
                    sm: "repeat(3, 1fr)",
                    md: "repeat(6, 1fr)",
                  },
                  gap: 1.5,
                }}
              >
                {Array.from({ length: 6 }, (_, index) => (
                  <Skeleton key={index} variant="rounded" height={84} />
                ))}
              </Box>
              <Skeleton variant="rounded" height={160} />
            </Stack>
          )}
          {!loading && error && (
            <Alert severity="error">
              {error}
              {data && " 以下保留上次加载的数据。"}
              <Button onClick={() => void load()}>重新加载</Button>
            </Alert>
          )}
          {!loading && !error && data && data.codes.total === 0 && (
            <Typography variant="body2" color="text.secondary">
              还没有兑换码。生成第一批后，这里会显示兑换统计。
            </Typography>
          )}
          {data && data.codes.total > 0 && (
            <Stack spacing={3}>
              <Box
                sx={{
                  display: "grid",
                  gridTemplateColumns: {
                    xs: "repeat(2, 1fr)",
                    sm: "repeat(3, 1fr)",
                    md: "repeat(6, 1fr)",
                  },
                  gap: 1.5,
                }}
              >
                <StatCard label="兑换码总数" value={String(data.codes.total)} />
                <StatCard
                  label="已兑换"
                  value={String(data.codes.redeemed)}
                  hint={`兑换率 ${redeemedRate}`}
                />
                <StatCard
                  label="兑换成功"
                  value={String(data.codes.succeeded)}
                  hint={`成功率 ${successRate}`}
                />
                <StatCard label="待处理" value={String(data.codes.processing)} />
                <StatCard label="待审核" value={String(data.codes.review)} />
                <StatCard label="已撤销" value={String(data.codes.revoked)} />
              </Box>
              {data.daily.length > 0 && (
                <Box component="section" aria-labelledby="stats-daily-title">
                  <Typography id="stats-daily-title" variant="h3" sx={{ mb: 1.5 }}>
                    最近 30 天活动
                  </Typography>
                  <ActivityChart daily={data.daily} />
                </Box>
              )}
              {data.months.length > 0 && (
                <Box component="section" aria-labelledby="stats-months-title">
                  <Typography id="stats-months-title" variant="h3" sx={{ mb: 1.5 }}>
                    套餐分布
                  </Typography>
                  <Stack spacing={1.5}>
                    {data.months.map((entry) => (
                      <Box key={entry.months}>
                        <Stack
                          direction="row"
                          justifyContent="space-between"
                          spacing={1}
                          sx={{ mb: 0.5 }}
                        >
                          <Typography variant="body2" fontWeight={600}>
                            {entry.months} 个月
                          </Typography>
                          <Typography variant="body2" color="text.secondary">
                            {entry.total} 枚 · 成功 {entry.succeeded}
                          </Typography>
                        </Stack>
                        <Box
                          aria-hidden
                          sx={{
                            display: "flex",
                            height: 10,
                            borderRadius: 5,
                            bgcolor: "action.hover",
                            overflow: "hidden",
                          }}
                        >
                          <Box
                            sx={{
                              width: `${(entry.succeeded / monthPeak) * 100}%`,
                              bgcolor: "success.main",
                            }}
                          />
                          <Box
                            sx={{
                              width: `${((entry.total - entry.succeeded) / monthPeak) * 100}%`,
                              bgcolor: "primary.main",
                            }}
                          />
                        </Box>
                      </Box>
                    ))}
                  </Stack>
                  <Typography variant="caption" color="text.secondary" component="p" sx={{ mt: 1 }}>
                    深色为成功部分，长度为各套餐占比。
                  </Typography>
                </Box>
              )}
              {data.review_stages.length > 0 && (
                <Box component="section" aria-labelledby="stats-stages-title">
                  <Typography id="stats-stages-title" variant="h3" sx={{ mb: 1.5 }}>
                    待审核阶段分布
                  </Typography>
                  <Stack spacing={1}>
                    {[...data.review_stages]
                      .sort((a, b) => a.progress - b.progress)
                      .map((stage) => (
                        <Stack
                          key={stage.progress}
                          direction="row"
                          alignItems="center"
                          spacing={1.5}
                        >
                          <Typography variant="body2" sx={{ minWidth: 72 }}>
                            阶段 {stage.progress}%
                          </Typography>
                          <Box
                            aria-hidden
                            sx={{
                              flex: 1,
                              height: 10,
                              borderRadius: 5,
                              bgcolor: "action.hover",
                              overflow: "hidden",
                            }}
                          >
                            <Box
                              sx={{
                                width: `${(stage.count / stagePeak) * 100}%`,
                                height: "100%",
                                bgcolor: "warning.main",
                              }}
                            />
                          </Box>
                          <Typography
                            variant="body2"
                            color="text.secondary"
                            sx={{ minWidth: 40, textAlign: "right" }}
                          >
                            {stage.count} 枚
                          </Typography>
                        </Stack>
                      ))}
                  </Stack>
                </Box>
              )}
            </Stack>
          )}
        </Box>
      </Collapse>
    </Paper>
  );
}
