import { ApiError, type Translator, useI18n } from "@edelweiss/sdk";
import { useNavigate, useSearchParams } from "@solidjs/router";
import { createEffect, createSignal, Show } from "solid-js";
import { LanguageSelect } from "../components/Navbar";
import { useShell } from "../context";

/** Control characters and backslashes: URL parsers strip or rewrite them, so "/<tab>/host" turns into "//host". */
function hasUnsafeCharacter(value: string): boolean {
  for (const ch of value) {
    const code = ch.codePointAt(0) ?? 0;
    if (code <= 0x1f || code === 0x7f || ch === "\\") return true;
  }
  return false;
}

/** Only local, non-login paths are allowed as redirect targets after sign-in. */
export function safeNext(next: string | undefined): string {
  // typeof: a repeated ?next=a&next=b arrives as an array.
  if (typeof next !== "string" || !next.startsWith("/") || hasUnsafeCharacter(next)) return "/";
  let url: URL;
  try {
    url = new URL(next, window.location.origin);
  } catch {
    return "/";
  }
  // What the URL parser makes of it is what counts: it must stay on this origin and must not lead back to the login.
  // It also resolves dot segments ("/.//host" becomes "//host"), and a path starting with "//" is read as another host
  // as soon as it is used as a link target.
  if (
    url.origin !== window.location.origin ||
    url.pathname.startsWith("/login") ||
    url.pathname.startsWith("//")
  )
    return "/";
  return url.pathname + url.search + url.hash;
}

function loginError(err: unknown, t: Translator["t"]): string {
  if (err instanceof ApiError) {
    if (err.code === "invalid_credentials") return t("login.invalid");
    if (err.code === "too_many_requests") return t("login.tooMany");
    return t("login.failed", { message: err.message });
  }
  return t("login.failed", { message: String(err) });
}

export function Login() {
  const shell = useShell();
  const { t } = useI18n("shell");
  const navigate = useNavigate();
  const [search] = useSearchParams<{ next?: string }>();
  const [username, setUsername] = createSignal("");
  const [password, setPassword] = createSignal("");
  const [error, setError] = createSignal<string>();
  const [busy, setBusy] = createSignal(false);

  createEffect(() => {
    if (shell.session.state() === "authenticated") navigate(safeNext(search.next), { replace: true });
  });

  const submit = async (e: SubmitEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(undefined);
    try {
      await shell.session.login(username(), password());
    } catch (err) {
      setError(loginError(err, t));
      setPassword("");
    } finally {
      setBusy(false);
    }
  };

  return (
    <main class="flex min-h-screen items-center justify-center p-4">
      <form class="card w-full max-w-sm bg-base-200 shadow" onSubmit={(e) => void submit(e)}>
        <div class="card-body gap-4">
          <div class="flex items-start justify-between gap-2">
            <div>
              <h1 class="card-title text-2xl">{t("login.title")}</h1>
              <p class="text-sm text-base-content/70">{t("login.subtitle")}</p>
            </div>
            <LanguageSelect />
          </div>
          <Show when={shell.session.expired()}>
            <div class="alert alert-info text-sm">{t("login.expired")}</div>
          </Show>
          <Show when={error()}>
            <div role="alert" class="alert alert-error text-sm">
              {error()}
            </div>
          </Show>
          <label class="flex flex-col gap-1">
            <span class="text-sm">{t("login.username")}</span>
            <input
              class="input w-full"
              name="username"
              autocomplete="username"
              required
              value={username()}
              onInput={(e) => setUsername(e.currentTarget.value)}
            />
          </label>
          <label class="flex flex-col gap-1">
            <span class="text-sm">{t("login.password")}</span>
            <input
              class="input w-full"
              type="password"
              name="password"
              autocomplete="current-password"
              required
              value={password()}
              onInput={(e) => setPassword(e.currentTarget.value)}
            />
          </label>
          <button type="submit" class="btn btn-primary" disabled={busy()}>
            <Show when={busy()}>
              <span class="loading loading-spinner loading-sm" />
            </Show>
            {t("login.submit")}
          </button>
        </div>
      </form>
    </main>
  );
}
