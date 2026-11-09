import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { readHealth, waitForRestart } from "./health";

const answer = (started: string, version = "0.3.1") =>
  new Response(JSON.stringify({ status: "ok", version, started }), { status: 200 });

beforeEach(() => {
  vi.useFakeTimers();
});
afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

/** Settles to what waitForRestart resolved with, or "pending" while it has not. */
function watch(p: Promise<string>) {
  const seen = { value: "pending" };
  void p.then((v) => {
    seen.value = v;
  });
  return seen;
}

describe("readHealth", () => {
  it("reads version and started", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => answer("2026-11-05T10:00:00Z")),
    );
    expect(await readHealth()).toEqual({ version: "0.3.1", started: "2026-11-05T10:00:00Z" });
  });

  it("is undefined when the host does not answer or answers oddly", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => {
        throw new TypeError("down");
      }),
    );
    expect(await readHealth()).toBeUndefined();
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => new Response("nope", { status: 502 })),
    );
    expect(await readHealth()).toBeUndefined();
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => new Response(JSON.stringify({ status: "ok" }), { status: 200 })),
    );
    expect(await readHealth()).toBeUndefined();
  });
});

describe("waitForRestart", () => {
  it("resolves on a new started without a failed probe", async () => {
    const answers = ["t1", "t1", "t2"];
    const health = vi.fn(async () => answer(answers.shift() ?? "t2"));
    vi.stubGlobal("fetch", health);
    const seen = watch(waitForRestart({ before: "t1" }));
    await vi.advanceTimersByTimeAsync(3000);
    await vi.advanceTimersByTimeAsync(3000);
    expect(seen.value).toBe("pending");
    await vi.advanceTimersByTimeAsync(3000);
    expect(seen.value).toBe("back");
    expect(health).toHaveBeenCalledTimes(3);
  });

  it("needs a failed probe first when before is undefined", async () => {
    let fail = false;
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => {
        if (fail) throw new TypeError("down");
        return answer("t1");
      }),
    );
    const seen = watch(waitForRestart({ before: undefined }));
    for (let i = 0; i < 5; i++) await vi.advanceTimersByTimeAsync(3000);
    expect(seen.value).toBe("pending"); // answering all the time: no restart seen
    fail = true;
    await vi.advanceTimersByTimeAsync(3000);
    expect(seen.value).toBe("pending");
    fail = false;
    await vi.advanceTimersByTimeAsync(3000);
    expect(seen.value).toBe("back");
  });

  it("does not take a probe that hangs for an answer, and keeps going", async () => {
    let hang = true;
    vi.stubGlobal(
      "fetch",
      vi.fn(
        (_url: string, init?: RequestInit) =>
          new Promise<Response>((resolve, reject) => {
            if (!hang) return resolve(answer("t2"));
            init?.signal?.addEventListener("abort", () => reject(new DOMException("t", "TimeoutError")));
          }),
      ),
    );
    const seen = watch(waitForRestart({ before: undefined, probeMs: 1000 }));
    await vi.advanceTimersByTimeAsync(3000 + 1000); // the probe hangs and times out: the host is down
    hang = false;
    await vi.advanceTimersByTimeAsync(3000);
    expect(seen.value).toBe("back");
  });

  it("times out", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => answer("t1")),
    );
    const seen = watch(waitForRestart({ before: "t1", timeoutMs: 20_000 }));
    await vi.advanceTimersByTimeAsync(18_000);
    expect(seen.value).toBe("pending");
    await vi.advanceTimersByTimeAsync(6_000);
    expect(seen.value).toBe("timeout");
  });

  it("times out when the host does not answer at all", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => {
        throw new TypeError("down");
      }),
    );
    const seen = watch(waitForRestart({ before: "t1", timeoutMs: 10_000 }));
    await vi.advanceTimersByTimeAsync(15_000);
    expect(seen.value).toBe("timeout");
  });

  it("stops when the signal is aborted", async () => {
    const health = vi.fn(async () => answer("t1"));
    vi.stubGlobal("fetch", health);
    const ctl = new AbortController();
    const seen = watch(waitForRestart({ before: "t1", signal: ctl.signal }));
    await vi.advanceTimersByTimeAsync(3000);
    ctl.abort();
    await vi.advanceTimersByTimeAsync(10_000);
    expect(seen.value).toBe("timeout");
    expect(health).toHaveBeenCalledTimes(1);
  });
});
