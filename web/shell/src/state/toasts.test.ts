import { afterEach, describe, expect, it, vi } from "vitest";
import { createToastStore } from "./toasts";

afterEach(() => vi.useRealTimers());

describe("toast store", () => {
  it("shows and auto-dismisses toasts", () => {
    vi.useFakeTimers();
    const toasts = createToastStore(1000);
    toasts.success("saved");
    toasts.error("failed");
    expect(toasts.items().map((t) => [t.kind, t.message])).toEqual([
      ["success", "saved"],
      ["error", "failed"],
    ]);
    toasts.dismiss(toasts.items()[0]!.id);
    expect(toasts.items()).toHaveLength(1);
    vi.advanceTimersByTime(1000);
    expect(toasts.items()).toHaveLength(0);
  });
});
