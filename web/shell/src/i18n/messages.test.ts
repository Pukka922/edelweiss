import { describe, expect, it } from "vitest";
import { de } from "./de";
import { en } from "./en";

function keys(dict: object, prefix = ""): string[] {
  return Object.entries(dict).flatMap(([k, v]) =>
    typeof v === "string" ? [`${prefix}${k}`] : keys(v as object, `${prefix}${k}.`),
  );
}

describe("shell messages", () => {
  it("German and English define the same keys", () => {
    expect(keys(de).sort()).toEqual(keys(en).sort());
  });
});
