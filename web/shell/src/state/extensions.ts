import {
  type ApiClient,
  type ExtensionInfo,
  type Locale,
  localized,
  SECTIONS,
  type Section,
} from "@edelweiss/sdk";
import { type Accessor, batch, createSignal } from "solid-js";

export interface ExtensionsStore {
  list: Accessor<ExtensionInfo[]>;
  /** True after the first load attempt finished. */
  loaded: Accessor<boolean>;
  error: Accessor<unknown>;
  reload(): Promise<void>;
  remove(id: string): Promise<void>;
  /** Forgets list, loaded state and error (the session ended); a load still in flight is ignored. */
  clear(): void;
}

export function createExtensionsStore(api: ApiClient): ExtensionsStore {
  const [list, setList] = createSignal<ExtensionInfo[]>([]);
  const [loaded, setLoaded] = createSignal(false);
  const [error, setError] = createSignal<unknown>();

  // Bumped by every reload() and by clear(): only the answer to the latest request is applied. An older one is
  // either overtaken by a newer load (answers can arrive in any order) or belongs to a session that is gone.
  let generation = 0;

  async function reload() {
    const mine = ++generation;
    try {
      const res = await api.get<{ extensions: ExtensionInfo[] }>("/extensions");
      if (mine !== generation) return;
      setList(res.extensions);
      setError(undefined);
    } catch (err) {
      if (mine !== generation) return;
      setError(() => err);
    } finally {
      if (mine === generation) setLoaded(true);
    }
  }

  async function remove(id: string) {
    try {
      await api.del(`/extensions/${encodeURIComponent(id)}`);
    } finally {
      // Also after a refusal: the core answers data_not_removed when the extension is gone but its data directory
      // could not be deleted, so the list has to be looked at as it is.
      await reload();
    }
  }

  function clear() {
    generation++;
    batch(() => {
      setList([]);
      setLoaded(false);
      setError(undefined);
    });
  }

  return { list, loaded, error, reload, remove, clear };
}

export interface SectionGroup {
  section: Section;
  items: ExtensionInfo[];
}

const sectionOf = (e: ExtensionInfo): Section =>
  (SECTIONS as readonly string[]).includes(e.menu.section) ? (e.menu.section as Section) : "other";

/** Menu groups in the fixed section order; inside a section by menu.order, then by name. */
export function groupBySection(list: readonly ExtensionInfo[], locale: Locale): SectionGroup[] {
  return SECTIONS.map((section) => ({
    section,
    items: list
      .filter((e) => sectionOf(e) === section)
      .sort(
        (a, b) =>
          a.menu.order - b.menu.order || localized(a.name, locale).localeCompare(localized(b.name, locale)),
      ),
  })).filter((g) => g.items.length > 0);
}
