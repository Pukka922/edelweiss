import type { Locale } from "@edelweiss/sdk";

export type Theme = "light" | "dark" | "system";

const THEME_KEY = "edelweiss.theme";
const LANG_KEY = "edelweiss.lang";

function storage(): Storage | undefined {
  try {
    return window.localStorage;
  } catch {
    return undefined;
  }
}

export function detectLocale(language: string | undefined): Locale {
  return language?.toLowerCase().startsWith("de") ? "de" : "en";
}

export function loadLocale(): Locale {
  const stored = storage()?.getItem(LANG_KEY);
  return stored === "de" || stored === "en" ? stored : detectLocale(navigator.language);
}

/**
 * Tells edelweissd (cookie) and the browser (<html lang>) which language is in use, without storing it as a
 * preference. The cookie tells the core which language extension backends should answer in; it is
 * session-scoped, so this has to run on every start.
 */
export function applyLocale(locale: Locale): void {
  // biome-ignore lint/suspicious/noDocumentCookie: the core reads this cookie (X-Edelweiss-Lang)
  document.cookie = `edelweiss_lang=${locale}; Path=/; SameSite=Strict`;
  document.documentElement.lang = locale;
}

/** Stores the user's language choice and applies it. */
export function saveLocale(locale: Locale): void {
  storage()?.setItem(LANG_KEY, locale);
  applyLocale(locale);
}

export function loadTheme(): Theme {
  const stored = storage()?.getItem(THEME_KEY);
  return stored === "light" || stored === "dark" ? stored : "system";
}

export function saveTheme(theme: Theme): void {
  if (theme === "system") storage()?.removeItem(THEME_KEY);
  else storage()?.setItem(THEME_KEY, theme);
  applyTheme(theme);
}

/** daisyUI themes: data-theme on <html>; without it the system preference (prefersdark) applies. */
export function applyTheme(theme: Theme, root: HTMLElement = document.documentElement): void {
  if (theme === "system") delete root.dataset.theme;
  else root.dataset.theme = theme;
}
