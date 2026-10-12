import {
  createI18n,
  type I18n,
  I18nContext,
  type Locale,
  SessionContext,
  ToastContext,
} from "@edelweiss/sdk";
import { type Accessor, createContext, createSignal, type JSX, useContext } from "solid-js";
import { de } from "./i18n/de";
import { en } from "./i18n/en";
import { applyLocale, loadLocale, loadTheme, saveLocale, saveTheme, type Theme } from "./prefs";
import { createExtensionsStore, type ExtensionsStore } from "./state/extensions";
import {
  type CheckMemory,
  createCheckMemory,
  createInstallerApi,
  type InstallerApi,
} from "./state/installer";
import { createSessionStore, type SessionStore } from "./state/session";
import { createToastStore, type ToastStore } from "./state/toasts";

export interface Shell {
  session: SessionStore;
  extensions: ExtensionsStore;
  installer: InstallerApi;
  /** Which update check the extensions page showed the errors of; it outlives the page. */
  updateChecks: CheckMemory;
  toasts: ToastStore;
  i18n: I18n;
  theme: Accessor<Theme>;
  setTheme(theme: Theme): void;
  setLocale(locale: Locale): void;
}

export const ShellContext = createContext<Shell>();

export function useShell(): Shell {
  const shell = useContext(ShellContext);
  if (!shell) throw new Error("useShell must be used inside ShellProviders");
  return shell;
}

export function createShell(options: { fetch?: typeof fetch; locale?: Locale; theme?: Theme } = {}): Shell {
  const locale = options.locale ?? loadLocale();
  // The edelweiss_lang cookie is session-scoped: write it on every start, not only when the locale changes,
  // or extension backends answer in the wrong language after a browser restart. Applying does not store the
  // (possibly auto-detected) language as a preference; only setLocale() does.
  applyLocale(locale);
  const i18n = createI18n(locale);
  i18n.register("shell", "en", en);
  i18n.register("shell", "de", de);
  const session = createSessionStore(options.fetch);
  const [theme, setThemeSignal] = createSignal<Theme>(options.theme ?? loadTheme());
  return {
    session,
    extensions: createExtensionsStore(session.api),
    installer: createInstallerApi(session.api),
    updateChecks: createCheckMemory(),
    toasts: createToastStore(),
    i18n,
    theme,
    setTheme(t) {
      setThemeSignal(t);
      saveTheme(t);
    },
    setLocale(l) {
      // cookie and <html lang> first: whatever reacts to the new locale (an extension asking its backend again,
      // for example) must already send the new language
      saveLocale(l);
      i18n.setLocale(l);
    },
  };
}

/** Provides the shell and the SDK contexts extensions rely on. */
export function ShellProviders(props: { shell: Shell; children: JSX.Element }): JSX.Element {
  return (
    <ShellContext.Provider value={props.shell}>
      <I18nContext.Provider value={props.shell.i18n}>
        <SessionContext.Provider value={props.shell.session}>
          <ToastContext.Provider value={props.shell.toasts}>{props.children}</ToastContext.Provider>
        </SessionContext.Provider>
      </I18nContext.Provider>
    </ShellContext.Provider>
  );
}
