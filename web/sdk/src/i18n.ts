import { flatten, resolveTemplate } from "@solid-primitives/i18n";
import { type Accessor, createContext, createSignal, useContext } from "solid-js";
import { ExtensionContext } from "./extension";
import type { Locale } from "./types";

export type Dict = { [key: string]: string | Dict };
export type Params = Record<string, string | number>;

export interface I18n {
  locale: Accessor<Locale>;
  setLocale(locale: Locale): void;
  /** Adds (or replaces) the messages of a namespace for one locale. */
  register(namespace: string, locale: Locale, dict: Dict): void;
  /** Message for key ("a.b"), with {{ param }} placeholders; falls back to en, then to the key. */
  translate(namespace: string, key: string, params?: Params): string;
}

export const I18nContext = createContext<I18n>();

const sdkMessages: Record<Locale, Dict> = {
  en: {
    loading: "Loading…",
    error: { title: "Something went wrong", retry: "Retry" },
    confirm: { cancel: "Cancel", confirm: "Confirm" },
    admin: { required: "Administrative access is required.", enable: "Enable admin mode" },
  },
  de: {
    loading: "Wird geladen…",
    error: { title: "Etwas ist schiefgelaufen", retry: "Erneut versuchen" },
    confirm: { cancel: "Abbrechen", confirm: "Bestätigen" },
    admin: { required: "Dafür ist administrativer Zugriff nötig.", enable: "Admin-Modus aktivieren" },
  },
};

export function createI18n(initial: Locale): I18n {
  const [locale, setLocale] = createSignal<Locale>(initial);
  const [version, setVersion] = createSignal(0);
  const dicts = new Map<string, Record<string, unknown>>();
  const i18n: I18n = {
    locale,
    setLocale: (l) => setLocale(() => l),
    register(namespace, l, dict) {
      dicts.set(`${namespace}:${l}`, flatten(dict) as Record<string, unknown>);
      setVersion((v) => v + 1);
    },
    translate(namespace, key, params) {
      version();
      const l = locale();
      const candidates = [dicts.get(`${namespace}:${l}`)?.[key], dicts.get(`${namespace}:en`)?.[key]];
      const message = candidates.find((m): m is string => typeof m === "string");
      if (message === undefined) return key;
      if (!params) return message;
      const args = Object.fromEntries(Object.entries(params).map(([k, v]) => [k, String(v)]));
      return resolveTemplate(message, args);
    },
  };
  for (const l of Object.keys(sdkMessages) as Locale[]) i18n.register("sdk", l, sdkMessages[l]);
  return i18n;
}

export interface Translator {
  t(key: string, params?: Params): string;
  locale: Accessor<Locale>;
  setLocale(locale: Locale): void;
}

/** Translator for namespace (default: the current extension's id, or "shell"). */
export function useI18n(namespace?: string): Translator {
  const i18n = useContext(I18nContext);
  if (!i18n) throw new Error("useI18n must be used inside the edelweiss shell");
  const ext = useContext(ExtensionContext);
  const ns = namespace ?? ext?.id ?? "shell";
  return {
    t: (key, params) => i18n.translate(ns, key, params),
    locale: i18n.locale,
    setLocale: i18n.setLocale,
  };
}
