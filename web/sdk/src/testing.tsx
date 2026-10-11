import type { JSX } from "solid-js";
import { ExtensionContext, extensionContextFor } from "./extension";
import { createI18n, type I18n, I18nContext } from "./i18n";
import { type SessionApi, SessionContext } from "./session";
import { fakeSession } from "./test-utils";
import { type ToastApi, ToastContext, type ToastKind } from "./toast";
import type { Locale } from "./types";

export { FakeEventSource, fakeSession, sessionInfo } from "./test-utils";

/** Toasts that only record what was shown. */
export function recordingToasts(): ToastApi & { shown: { kind: ToastKind; message: string }[] } {
  const shown: { kind: ToastKind; message: string }[] = [];
  const show = (kind: ToastKind, message: string) => {
    shown.push({ kind, message });
  };
  return {
    shown,
    show,
    success: (m) => show("success", m),
    error: (m) => show("error", m),
    info: (m) => show("info", m),
  };
}

/** Provides the shell contexts the SDK expects, for component tests. */
export function TestShell(props: {
  children: JSX.Element;
  session?: SessionApi;
  i18n?: I18n;
  locale?: Locale;
  extension?: string;
  toasts?: ToastApi;
}): JSX.Element {
  const i18n = props.i18n ?? createI18n(props.locale ?? "en");
  const session = props.session ?? fakeSession().api;
  const toasts = props.toasts ?? recordingToasts();
  const content = () =>
    props.extension ? (
      <ExtensionContext.Provider value={extensionContextFor(props.extension)}>
        {props.children}
      </ExtensionContext.Provider>
    ) : (
      props.children
    );
  return (
    <I18nContext.Provider value={i18n}>
      <SessionContext.Provider value={session}>
        <ToastContext.Provider value={toasts}>{content()}</ToastContext.Provider>
      </SessionContext.Provider>
    </I18nContext.Provider>
  );
}
