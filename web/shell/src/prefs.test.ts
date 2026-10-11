import { afterEach, describe, expect, it } from "vitest";
import { applyLocale, applyTheme, detectLocale, loadLocale, loadTheme, saveLocale, saveTheme } from "./prefs";

afterEach(() => {
  localStorage.clear();
  delete document.documentElement.dataset.theme;
  // biome-ignore lint/suspicious/noDocumentCookie: removes the cookie applyLocale() wrote
  document.cookie = "edelweiss_lang=; Path=/; Max-Age=0";
  document.documentElement.removeAttribute("lang");
});

describe("prefs", () => {
  it("detects the locale from the browser language", () => {
    expect(detectLocale("de-DE")).toBe("de");
    expect(detectLocale("en-US")).toBe("en");
    expect(detectLocale("fr")).toBe("en");
    expect(detectLocale(undefined)).toBe("en");
  });

  it("stores the locale and forwards it to extensions via cookie", () => {
    saveLocale("de");
    expect(loadLocale()).toBe("de");
    expect(document.cookie).toContain("edelweiss_lang=de");
    expect(document.documentElement.lang).toBe("de");
  });

  it("applies the locale without storing it as a preference", () => {
    applyLocale("de");
    expect(document.cookie).toContain("edelweiss_lang=de");
    expect(document.documentElement.lang).toBe("de");
    expect(localStorage.getItem("edelweiss.lang")).toBeNull();
    expect(loadLocale()).toBe("en"); // browser language of jsdom, not the applied one
  });

  it("stores and applies the theme", () => {
    expect(loadTheme()).toBe("system");
    saveTheme("dark");
    expect(loadTheme()).toBe("dark");
    expect(document.documentElement.dataset.theme).toBe("dark");
    saveTheme("system");
    expect(loadTheme()).toBe("system");
    expect(document.documentElement.dataset.theme).toBeUndefined();
    const el = document.createElement("div");
    applyTheme("light", el);
    expect(el.dataset.theme).toBe("light");
  });
});
