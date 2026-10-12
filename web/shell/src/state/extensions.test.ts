import { createApiClient } from "@edelweiss/sdk";
import { describe, expect, it } from "vitest";
import { fakeFetch, json } from "../test/fake-fetch";
import { ext } from "../test/fixtures";
import { createExtensionsStore, groupBySection } from "./extensions";

describe("groupBySection", () => {
  it("orders by section, then order, then name; unknown sections go to other", () => {
    const groups = groupBySection(
      [
        ext("b", "tools", 2),
        ext("a", "tools", 2),
        ext("sys", "system", 50),
        ext("x", "games", 1),
        ext("c", "tools", 1),
      ],
      "en",
    );
    expect(groups.map((g) => g.section)).toEqual(["system", "tools", "other"]);
    expect(groups[1]!.items.map((e) => e.id)).toEqual(["c", "a", "b"]);
    expect(groups[2]!.items.map((e) => e.id)).toEqual(["x"]);
  });

  it("puts virtualization between storage and network", () => {
    const groups = groupBySection(
      [ext("n", "network", 1), ext("v", "virtualization", 1), ext("s", "storage", 1), ext("t", "tools", 1)],
      "en",
    );
    expect(groups.map((g) => g.section)).toEqual(["storage", "virtualization", "network", "tools"]);
  });
});

describe("extensions store", () => {
  it("loads the list and removes extensions with the CSRF token", async () => {
    let list = [ext("system", "system", 10)];
    const fetch = fakeFetch({
      "GET /api/extensions": () => json(200, { extensions: list }),
      "DELETE /api/extensions/system": () => {
        list = [];
        return new Response(null, { status: 204 });
      },
    });
    const store = createExtensionsStore(createApiClient({ base: "/api", csrfToken: () => "t-1", fetch }));
    expect(store.loaded()).toBe(false);
    await store.reload();
    expect(store.loaded()).toBe(true);
    expect(store.list().map((e) => e.id)).toEqual(["system"]);
    await store.remove("system");
    expect(fetch.calls.find((c) => c.method === "DELETE")?.headers["X-Csrf-Token"]).toBe("t-1");
    expect(store.list()).toEqual([]);
  });

  it("reloads the list after a removal that failed halfway, and rejects with the answer", async () => {
    // data_not_removed: the extension is gone, only its data directory could not be deleted
    let list = [ext("system", "system", 10)];
    const message =
      "the extension was removed, but its data directory /var/lib/edelweiss-ext-system could not be deleted";
    const fetch = fakeFetch({
      "GET /api/extensions": () => json(200, { extensions: list }),
      "DELETE /api/extensions/system": () => {
        list = [];
        return json(500, { error: { code: "data_not_removed", message } });
      },
    });
    const store = createExtensionsStore(createApiClient({ base: "/api", csrfToken: () => "t-1", fetch }));
    await store.reload();
    expect(store.list()).toHaveLength(1);
    await expect(store.remove("system")).rejects.toMatchObject({ code: "data_not_removed", message });
    expect(store.list()).toEqual([]);
  });

  it("forgets list, loaded state and error on clear", async () => {
    let answer = () => json(200, { extensions: [ext("system", "system", 10)] });
    const fetch = fakeFetch({ "GET /api/extensions": () => answer() });
    const store = createExtensionsStore(createApiClient({ base: "/api", csrfToken: () => undefined, fetch }));
    await store.reload();
    expect(store.list()).toHaveLength(1);
    store.clear();
    expect(store.list()).toEqual([]);
    expect(store.loaded()).toBe(false);

    answer = () => json(500, { error: { code: "internal", message: "boom" } });
    await store.reload();
    expect(store.error()).toBeTruthy();
    store.clear();
    expect(store.error()).toBeUndefined();
    expect(store.loaded()).toBe(false);
  });

  it("ignores a load that was still in flight when the list was cleared", async () => {
    let release = () => {};
    const gate = new Promise<void>((resolve) => {
      release = resolve;
    });
    const fetch = fakeFetch({
      "GET /api/extensions": async () => {
        await gate;
        return json(200, { extensions: [ext("system", "system", 10)] });
      },
    });
    const store = createExtensionsStore(createApiClient({ base: "/api", csrfToken: () => undefined, fetch }));
    const loading = store.reload();
    store.clear();
    release();
    await loading;
    expect(store.list()).toEqual([]);
    expect(store.loaded()).toBe(false);
  });

  it("applies the answer of the latest load, however the answers are ordered", async () => {
    const answers: ((names: string[]) => void)[] = [];
    const fetch = fakeFetch({
      "GET /api/extensions": () =>
        new Promise<Response>((resolve) => {
          answers.push((names) => resolve(json(200, { extensions: names.map((n) => ext(n, "tools", 1)) })));
        }),
    });
    const store = createExtensionsStore(createApiClient({ base: "/api", csrfToken: () => undefined, fetch }));
    const first = store.reload();
    const second = store.reload();
    expect(answers).toHaveLength(2);

    answers[1]!(["newer"]);
    await second;
    expect(store.list().map((e) => e.id)).toEqual(["newer"]);
    expect(store.loaded()).toBe(true);

    answers[0]!(["older"]); // the first request finishes last and must not overwrite the newer list
    await first;
    expect(store.list().map((e) => e.id)).toEqual(["newer"]);
    expect(store.loaded()).toBe(true);
  });

  it("does not let an older failure replace the newer list", async () => {
    const answers: ((res: Response) => void)[] = [];
    const fetch = fakeFetch({
      "GET /api/extensions": () => new Promise<Response>((resolve) => answers.push(resolve)),
    });
    const store = createExtensionsStore(createApiClient({ base: "/api", csrfToken: () => undefined, fetch }));
    const first = store.reload();
    const second = store.reload();
    answers[1]!(json(200, { extensions: [ext("newer", "tools", 1)] }));
    await second;
    answers[0]!(json(500, { error: { code: "internal", message: "boom" } }));
    await first;
    expect(store.list().map((e) => e.id)).toEqual(["newer"]);
    expect(store.error()).toBeUndefined();
  });

  it("records load errors", async () => {
    const store = createExtensionsStore(
      createApiClient({ base: "/api", csrfToken: () => undefined, fetch: fakeFetch({}) }),
    );
    await store.reload();
    expect(store.loaded()).toBe(true);
    expect(store.error()).toBeTruthy();
  });
});
