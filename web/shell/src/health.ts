/** What GET /api/health tells about the running edelweissd. */
export type HealthInfo = { version: string; started: string };

type Probe = { up: boolean; info?: HealthInfo };

async function probe(signal?: AbortSignal): Promise<Probe> {
  try {
    const res = await fetch("/api/health", { cache: "no-store", signal });
    if (!res.ok) return { up: false };
    try {
      const body = (await res.json()) as Partial<HealthInfo>;
      if (typeof body.version === "string" && typeof body.started === "string") {
        return { up: true, info: { version: body.version, started: body.started } };
      }
    } catch {
      // an answer without the details still says that the host is up
    }
    return { up: true };
  } catch {
    return { up: false };
  }
}

/** The version and the start time of the running edelweissd; undefined when it does not answer properly. */
export async function readHealth(signal?: AbortSignal): Promise<HealthInfo | undefined> {
  return (await probe(signal)).info;
}

/**
 * Waits until edelweissd runs again after a restart it was asked for.
 *
 * With `before` (the `started` seen before the action), a probe that answers with another `started` is the new
 * process. Without it, the restart is seen as one failed probe (the host went down) followed by an answer.
 *
 * It gives up after timeoutMs (default 3 minutes), checked after each probe. Without `before`, only an answering host
 * runs into the timeout: one that is down is still restarting. An aborted signal ends the wait with "timeout" too;
 * the caller that aborted knows why.
 *
 * pollMs is the time between two probes, probeMs how long one may take: a request that hangs (the host dropped off the
 * network) must not stall the loop.
 */
export function waitForRestart(opts: {
  before: string | undefined;
  pollMs?: number;
  probeMs?: number;
  timeoutMs?: number;
  signal?: AbortSignal;
}): Promise<"back" | "timeout"> {
  const { before, signal } = opts;
  const pollMs = opts.pollMs ?? 3000;
  const probeMs = opts.probeMs ?? 5000;
  const timeoutMs = opts.timeoutMs ?? 180_000;
  const startedAt = Date.now();
  return new Promise((resolve) => {
    let timer: ReturnType<typeof setTimeout> | undefined;
    let done = false;
    let wentDown = false;
    const finish = (result: "back" | "timeout") => {
      if (done) return;
      done = true;
      clearTimeout(timer);
      signal?.removeEventListener("abort", onAbort);
      resolve(result);
    };
    const onAbort = () => finish("timeout");
    if (signal?.aborted) return finish("timeout");
    signal?.addEventListener("abort", onAbort);

    const next = () => {
      timer = setTimeout(async () => {
        // the timeout keeps a request that hangs from stalling the loop
        const limit = new AbortController();
        const cutOff = setTimeout(() => limit.abort(), probeMs);
        const answer = await probe(limit.signal);
        clearTimeout(cutOff);
        if (done) return;
        const restarted =
          before === undefined
            ? answer.up && wentDown
            : answer.info !== undefined && answer.info.started !== before;
        if (restarted) return finish("back");
        if (!answer.up) wentDown = true;
        if (Date.now() - startedAt > timeoutMs && (before !== undefined || answer.up))
          return finish("timeout");
        next();
      }, pollMs);
    };
    next();
  });
}
