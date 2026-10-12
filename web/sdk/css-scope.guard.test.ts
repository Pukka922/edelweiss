// @vitest-environment node
import type { Selector } from "lightningcss";
import { transform } from "lightningcss";
import { describe, expect, it, vi } from "vitest";
import { scopeCss } from "./css-scope";

// scopeCss checks its list of the selectors of an @scope rule when it is used up. Real CSS cannot reach the check that
// listed selectors were never visited: lightningcss visits every selector it parses, before it minifies, also those of
// empty and merged rules. So these tests put a transform in place of lightningcss's that walks an @scope rule the way
// scopeCss expects, or leaves a selector out.
vi.mock("lightningcss", () => ({ transform: vi.fn() }));

interface Walked {
  Rule: { scope: (rule: unknown) => void };
  Selector: (selector: Selector) => unknown;
}

const sel = (name: string): Selector => [{ type: "class", name }];
const start = sel("a");
const inside = [sel("b"), sel("c")];
// @scope (.a){.b,.c{…}}: scopeCss lists a, b, c
const scopeRule = {
  type: "scope",
  value: {
    scopeStart: [start],
    scopeEnd: null,
    rules: [{ type: "style", value: { selectors: inside, rules: [] } }],
  },
};

/** Lets the transform visit the @scope rule, then exactly these selectors. */
function visit(...selectors: Selector[]) {
  vi.mocked(transform).mockImplementation(((options: { visitor: Walked }) => {
    options.visitor.Rule.scope(scopeRule);
    for (const s of selectors) options.visitor.Selector(s);
    return { code: Buffer.from("done") };
  }) as unknown as typeof transform);
}

describe("scopeCss: the selectors listed for an @scope rule", () => {
  it("passes when lightningcss visits them all, in order", () => {
    visit(start, ...inside);
    expect(scopeCss("ignored{}", "docker")).toBe("done");
  });
  it("throws when the last of them is never visited", () => {
    visit(start, sel("b"));
    expect(() => scopeCss("ignored{}", "docker")).toThrow(
      "lost track of the selectors of an @scope or @layer theme rule",
    );
  });
  it("throws when none of them is visited", () => {
    visit();
    expect(() => scopeCss("ignored{}", "docker")).toThrow(
      "lost track of the selectors of an @scope or @layer theme rule",
    );
  });
  it("throws when another selector than the next one is visited", () => {
    visit(start, sel("c"));
    expect(() => scopeCss("ignored{}", "docker")).toThrow(
      "lost track of the selectors of an @scope or @layer theme rule",
    );
  });
});
