// Retry only reads of the existing ticket. Never resubmit an order during a restart.
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
      if (result.status !== 429 && (result.status === undefined || result.status < 500)) return result;
      if (now() >= deadline) return result;
    } catch (error) {
      signal.throwIfAborted();
      if (now() >= deadline) throw error;
    }
    options.onRetry?.();
    await pause();
  }
}
