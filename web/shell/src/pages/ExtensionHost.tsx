import {
  AdminRequired,
  ApiError,
  type Dict,
  ErrorAlert,
  ExtensionContext,
  type ExtensionInfo,
  type ExtensionModule,
  extensionContextFor,
  extensionScopeClass,
  type I18n,
  isExtensionModule,
  LOCALES,
  LoadingState,
  localized,
  useI18n,
} from "@edelweiss/sdk";
import { useParams } from "@solidjs/router";
import { createMemo, createResource, ErrorBoundary, Match, Switch } from "solid-js";
import { Dynamic } from "solid-js/web";
import { useShell } from "../context";
import { apkAddCommand } from "../state/installer";

export type ModuleLoader = (url: string) => Promise<unknown>;

const importModule: ModuleLoader = (url) => import(/* @vite-ignore */ url);

class NotAModuleError extends Error {
  constructor(readonly id: string) {
    super(`${id} does not export defineExtension(...)`);
  }
}

const modules = new Map<string, Promise<ExtensionModule>>();

function ensureStylesheet(href: string) {
  if (document.querySelector(`link[data-ext-style="${CSS.escape(href)}"]`)) return;
  const link = document.createElement("link");
  link.rel = "stylesheet";
  link.href = href;
  link.dataset.extStyle = href;
  document.head.append(link);
}

/** Loads an extension's styles, messages and module; modules are cached per URL. */
export async function loadExtension(
  ext: ExtensionInfo,
  i18n: I18n,
  loader: ModuleLoader,
  fetchImpl: typeof fetch = (input, init) => fetch(input, init),
): Promise<ExtensionModule> {
  const ui = ext.ui;
  if (!ui) throw new Error(`${ext.id} has no user interface`);
  for (const href of ui.styles) ensureStylesheet(href);
  if (ui.locales) {
    await Promise.all(
      LOCALES.map(async (locale) => {
        const res = await fetchImpl(`${ui.locales}/${locale}.json`, { credentials: "same-origin" });
        if (res.status === 401) throw new ApiError(401, "unauthenticated", "login required");
        if (res.ok) i18n.register(ext.id, locale, (await res.json()) as Dict);
      }),
    );
  }
  // the version parameter makes a reinstalled extension load fresh code
  const url = `${ui.entry}?v=${ext.source.sha256.slice(0, 12)}`;
  let module = modules.get(url);
  if (!module) {
    module = loader(url).then((m) => {
      const mod = (m as { default?: unknown } | null)?.default;
      if (!isExtensionModule(mod)) throw new NotAModuleError(ext.id);
      return mod;
    });
    modules.set(url, module);
    module.catch(() => modules.delete(url));
  }
  return module;
}

const buildKey = (e: ExtensionInfo | undefined) =>
  e?.ui ? `${e.id}|${e.ui.entry}|${e.source.sha256}` : undefined;

export function ExtensionHost(props: { loader?: ModuleLoader }) {
  const params = useParams<{ ext: string }>();
  const shell = useShell();
  const { t } = useI18n("shell");
  const ext = () => shell.extensions.list().find((e) => e.id === params.ext);
  const blocked = () => {
    const e = ext();
    return !!e && e.requiresAdmin && !shell.session.admin();
  };
  const missing = () => ext()?.missingPackages ?? [];
  // Compared by what determines the loaded code, not by object identity: reloading the extension list
  // (e.g. on window focus) replaces every ExtensionInfo, and that must not tear down a running UI.
  const loadable = createMemo(
    () => {
      const e = ext();
      return e?.ui && !blocked() && missing().length === 0 ? e : undefined;
    },
    undefined,
    { equals: (a, b) => buildKey(a) === buildKey(b) },
  );
  const [module, { refetch }] = createResource(loadable, async (e) => {
    try {
      return await loadExtension(e, shell.i18n, props.loader ?? importModule);
    } catch (err) {
      // Loading can fail because the session is gone: a locale file answers 401, and an entry module that answers
      // 401 only shows up as a failed import. Asking the core lets the shell show the login page; a module that
      // loaded but is not an extension has nothing to do with the session.
      if (!(err instanceof NotAModuleError)) void shell.session.refresh().catch(() => {});
      throw err;
    }
  });
  const describe = (err: unknown) =>
    err instanceof NotAModuleError
      ? t("ext.notAModule", { id: err.id })
      : t("ext.loadFailed", { id: params.ext, message: err instanceof Error ? err.message : String(err) });

  return (
    <Switch fallback={<LoadingState />}>
      <Match when={!shell.extensions.loaded()}>
        <LoadingState />
      </Match>
      <Match when={!ext()}>
        <ErrorAlert error={t("ext.notFound")} />
      </Match>
      <Match when={missing().length > 0}>
        <div role="alert" class="alert alert-warning m-6 flex flex-col items-start gap-2">
          <h2 class="font-semibold">{t("ext.missingPackagesTitle")}</h2>
          <p>
            {t("ext.missingPackages", {
              name: localized(ext()?.name, shell.i18n.locale()),
              packages: missing().join(", "),
            })}
          </p>
          <code class="rounded bg-base-200 px-2 py-1 font-mono text-base-content">
            {apkAddCommand(missing())}
          </code>
        </div>
      </Match>
      <Match when={blocked()}>
        <AdminRequired />
      </Match>
      <Match when={!ext()?.ui}>
        <p class="p-6 text-base-content/70">{t("ext.noUi")}</p>
      </Match>
      <Match when={module.state === "errored"}>
        <ErrorAlert error={describe(module.error)} onRetry={() => void refetch()} />
      </Match>
      <Match when={module.state === "ready" ? module() : undefined} keyed>
        {(mod) => (
          <ExtensionContext.Provider value={extensionContextFor(params.ext)}>
            {/* the extension's style sheet is scoped to this element (SDK 0.2.0) */}
            <div class={extensionScopeClass(params.ext)}>
              <ErrorBoundary fallback={(err, reset) => <ErrorAlert error={err} onRetry={reset} />}>
                <Dynamic component={mod.component} />
              </ErrorBoundary>
            </div>
          </ExtensionContext.Provider>
        )}
      </Match>
    </Switch>
  );
}
