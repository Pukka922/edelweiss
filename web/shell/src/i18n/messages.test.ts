import { describe, expect, it } from "vitest";
import { de } from "./de";
import { en } from "./en";

function keys(dict: object, prefix = ""): string[] {
  return Object.entries(dict).flatMap(([k, v]) =>
    typeof v === "string" ? [`${prefix}${k}`] : keys(v as object, `${prefix}${k}.`),
  );
}

function flat(dict: object, prefix = ""): Record<string, string> {
  return Object.fromEntries(
    Object.entries(dict).flatMap(([k, v]) =>
      typeof v === "string" ? [[`${prefix}${k}`, v]] : Object.entries(flat(v as object, `${prefix}${k}.`)),
    ),
  );
}

describe("shell messages", () => {
  it("German and English define the same keys", () => {
    expect(keys(de).sort()).toEqual(keys(en).sort());
  });

  // a wait state ends in a spaced ellipsis, as in "Checking the update …"
  it("writes the ellipsis of the about page with a space before it", () => {
    for (const dict of [en.about, de.about]) {
      for (const [key, text] of Object.entries(flat(dict))) {
        expect(text, key).not.toMatch(/[^\s]…/);
      }
    }
  });
});
