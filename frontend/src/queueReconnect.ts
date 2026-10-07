// Retry only reads of the existing ticket. Never resubmit an order during a restart.
// Network errors, rate limits and gateway/unavailable responses are transient;
// every other status (including stored final failures) is shown to the user.
export function transientQueueStatus(status?: number) {
  return status === 429 || status === 502 || status === 503 || status === 504;
}
export async function readQueueWithReconnect<T>(
  read: () => Promise<{ ok: boolean; data: T; status?: number }>,
  signal: AbortSignal,
  pause: () => Promise<void> = () => new Promise((resolve) => setTimeout(resolve, 3000)),
  now: () => number = Date.now,
  options: { maxWaitMs?: number; onRetry?: () => void } = {},
): Promise<{ ok: boolean; data: T; status?: number }> {
  const deadline = now() + (options.maxWaitMs ?? Number.POSITIVE_INFINITY);
  for (;;) {
    signal.throwIfAborted();
    try {
      const result = await read();
      if (!transientQueueStatus(result.status)) return result;
      if (now() >= deadline) return result;
    } catch (error) {
      signal.throwIfAborted();
      if (now() >= deadline) throw error;
    }
    options.onRetry?.();
    await pause();
  }
}
