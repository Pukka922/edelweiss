import { describe, expect, it } from "vitest";
import { defineExtension, extensionContextFor, isExtensionModule } from "./extension";
import { localized } from "./types";

describe("extension helpers", () => {
  it("defineExtension returns the module and isExtensionModule recognises it", () => {
    const mod = defineExtension({ component: () => null });
    expect(isExtensionModule(mod)).toBe(true);
    expect(isExtensionModule({})).toBe(false);
    expect(isExtensionModule(null)).toBe(false);
  });

  it("derives paths from the id", () => {
    expect(extensionContextFor("system")).toEqual({
      id: "system",
      basePath: "/x/system",
      apiBase: "/ext/system/api",
    });
  });

  it("localized picks the locale, then en, then any value", () => {
    expect(localized({ de: "Übersicht", en: "Overview" }, "de")).toBe("Übersicht");
    expect(localized({ en: "Overview" }, "de")).toBe("Overview");
    expect(localized({ fr: "Aperçu" }, "de")).toBe("Aperçu");
    expect(localized(undefined, "de")).toBe("");
  });
});
