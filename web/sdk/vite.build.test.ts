// @vitest-environment node
import path from "node:path";
import { build } from "vite";
import { describe, expect, it } from "vitest";
import { edelweissExtension } from "./vite";

interface Output {
  type: string;
  fileName: string;
  source?: string | Uint8Array;
}

describe("edelweissExtension build", () => {
  it("writes a style.css scoped to the extension's container", async () => {
    const root = path.join(__dirname, "test-fixtures/scope-ext/ui");
    const out = (await build({
      root,
      logLevel: "silent",
      plugins: [edelweissExtension()],
      build: { write: false },
    })) as { output: Output[] } | { output: Output[] }[];
    const outputs = (Array.isArray(out) ? out : [out]).flatMap((o) => o.output);
    const css = outputs.find((o) => o.fileName === "style.css");
    const text = typeof css?.source === "string" ? css.source : new TextDecoder().decode(css?.source);
    expect(text).toContain(":where(.edelweiss-ext-scopetest) .badge");
    expect(text).toMatch(/(^|[;}{,]):root:has\(/);
    expect(text).not.toMatch(/(^|[}{,])\.badge\{/);
    // the variable of the extension's own @theme sits on the container, not on :root or :host
    expect(text).toContain("@layer theme{:where(.edelweiss-ext-scopetest){--color-brand:red}}");
    expect(text).not.toMatch(/(:root|:host)[^{}]*\{[^{}]*--color-brand/);
  }, 60_000);
});
