import type { Rule, Selector, SelectorComponent } from "lightningcss";
import { transform } from "lightningcss";
import { extensionScopeClass } from "./src/scope";

const descendant: SelectorComponent = { type: "combinator", value: "descendant" };

/**
 * The browsers Tailwind 4.3.3 itself compiles for (the lightningcss targets in @tailwindcss/node): Safari and iOS 16.4,
 * Firefox 128, Chrome 111. Printing the sheet again with the same targets keeps its vendor prefixes and fallbacks.
 */
const targets = {
  safari: (16 << 16) | (4 << 8),
  ios_saf: (16 << 16) | (4 << 8),
  firefox: 128 << 16,
  chrome: 111 << 16,
};

const lostTrack = "scopeCss lost track of the selectors of an @scope or @layer theme rule";

/** Whether a compound selector targets the page itself: it starts with :root, :host, html, body or [data-theme…]. */
function isPage(compound: SelectorComponent[]): boolean {
  const first = compound[0];
  if (!first) return false;
  if (first.type === "type") return first.name === "html" || first.name === "body";
  if (first.type === "pseudo-class") return first.kind === "root" || first.kind === "host";
  if (first.type === "attribute") return first.name === "data-theme";
  return false;
}

/** Whether a selector is just :root or :host (without an argument). */
function isRootOrHost([only, ...rest]: Selector): boolean {
  if (rest.length > 0 || only?.type !== "pseudo-class") return false;
  return only.kind === "root" || (only.kind === "host" && !only.selectors);
}

/** Whether a selector refers to its parent rule with & (lightningcss adds it to a nested rule written without one). */
function isNested(node: unknown): boolean {
  if (Array.isArray(node)) return node.some(isNested);
  if (typeof node !== "object" || node === null) return false;
  return (node as { type?: string }).type === "nesting" || Object.values(node).some(isNested);
}

function scopeSelector(selector: Selector, where: SelectorComponent): Selector {
  const types = selector.map((c) => c.type);
  const last = types.lastIndexOf("combinator");
  if (isPage(selector.slice(last + 1))) return selector; // the page itself: global
  // The container goes behind the last of the leading page compounds (html body .x: behind body); `lead` is the index
  // of the combinator that follows them.
  let lead = 0;
  for (let from = 0; ; ) {
    const next = types.indexOf("combinator", from);
    if (next === -1 || !isPage(selector.slice(from, next))) break;
    lead = next;
    from = next + 1;
  }
  if (lead === 0) return [where, descendant, ...selector];
  return [...selector.slice(0, lead), descendant, where, ...selector.slice(lead)];
}

/**
 * What to do with a selector that lightningcss visits. "normal": scope it like any other. "start": the scope start of
 * an @scope rule, scoped. "keep": leave it (the limit and the rules inside an @scope rule). "container": a :root or
 * :host in @layer theme, replaced by the container itself. "drop": a further :root or :host of the same rule there.
 */
type Action = "normal" | "start" | "keep" | "container" | "drop";

/** The part of a rule that holds selectors and rules: a style rule, an @scope rule or the style rule of a @nest rule. */
interface RuleBody {
  selectors?: Selector[];
  scopeStart?: Selector[] | null;
  scopeEnd?: Selector[] | null;
  rules?: Rule[];
}

/** Whether a rule is `@layer theme { … }`, the layer Tailwind puts the variables of @theme in. */
function isThemeLayer(rule: Rule): boolean {
  if (rule.type !== "layer-block") return false;
  const name = rule.value.name;
  return name?.length === 1 && name[0] === "theme";
}

/**
 * The selectors lightningcss visits for these rules, in its order (those of a rule, then those of the rules inside),
 * each with its action. `inTheme` is set inside `@layer theme`, `keep` inside an @scope rule.
 */
function listSelectors(rules: Rule[] | undefined, inTheme: boolean, keep: boolean): [Selector, Action][] {
  return (rules ?? []).flatMap((rule) => {
    if (rule.type === "page") return []; // its selectors are page selectors (:first), not a list of selectors
    const value = (rule as { value?: RuleBody & { style?: RuleBody } }).value;
    const body = rule.type === "nesting" ? value?.style : value;
    const listed: [Selector, Action][] = [];
    for (const s of body?.scopeStart ?? []) listed.push([s, keep ? "keep" : "start"]);
    for (const s of body?.scopeEnd ?? []) listed.push([s, "keep"]);
    let container = false;
    for (const s of body?.selectors ?? []) {
      if (keep) {
        listed.push([s, "keep"]);
      } else if (inTheme && isRootOrHost(s)) {
        listed.push([s, container ? "drop" : "container"]);
        container = true;
      } else {
        listed.push([s, "normal"]);
      }
    }
    listed.push(...listSelectors(body?.rules, inTheme || isThemeLayer(rule), keep || rule.type === "scope"));
    return listed;
  });
}

/**
 * Scopes an extension's style sheet to the element the shell renders the extension in: every selector gets
 * :where(.edelweiss-ext-<id>) in front (specificity 0, so the order of the rules keeps deciding), the page's own rules
 * (:root, html, body, :host, [data-theme]) and @keyframes, @property, @font-face stay global. The exception is @layer
 * theme, where Tailwind puts the variables of an extension's @theme: its :root and :host are replaced by the container
 * itself, so the variables apply to the extension only and do not override the shell's. Of an @scope rule only the
 * scope start gets the container: the limit and the rules inside are relative to it. Throws on CSS it cannot parse, so
 * a build never ships an unscoped sheet. The sheet is printed again by lightningcss (minified, for Tailwind's browser
 * targets), which drops @charset and comments such as Vite's marker, except
 * licence comments (/*! …) at its start.
 */
export function scopeCss(css: string, id: string, filename = "style.css"): string {
  const where: SelectorComponent = {
    type: "pseudo-class",
    kind: "where",
    selectors: [[{ type: "class", name: extensionScopeClass(id) }]],
  };
  // Only selectors go through the visitor and back into lightningcss, which cannot take a whole @container, @property
  // or @font-face back from a StyleSheet visitor. It visits the selector list of every style rule, in @media,
  // @supports, @layer … and @scope as well, but not keyframe selectors and not the selectors inside :is(), :not() and
  // :has(). A nested rule is relative to its parent and needs no container of its own.
  //
  // It does not tell when it leaves a rule, so an @scope rule or @layer theme (visited before its selectors) lists the
  // selectors that follow, each with its action. The list predicts the order of lightningcss and is checked at both
  // ends: a selector other than the next listed one throws, and so does a list that is not used up after the
  // transform. A selector visited after the list ran out is not noticed: it is scoped like one outside.
  const upcoming: { key: string; action: Action }[] = [];
  const list = (rule: Rule) => {
    if (upcoming.length > 0) return; // inside a listed rule
    for (const [selector, action] of listSelectors([rule], false, false)) {
      upcoming.push({ key: JSON.stringify(selector), action });
    }
  };
  const noSelector: Selector[] = [];
  const { code } = transform({
    filename,
    code: Buffer.from(css),
    minify: true,
    targets,
    errorRecovery: false,
    visitor: {
      Rule: {
        scope: list,
        "layer-block": (rule) => {
          if (isThemeLayer(rule)) list(rule);
        },
      },
      Selector: (selector) => {
        const next = upcoming.shift();
        if (next && next.key !== JSON.stringify(selector)) throw new Error(lostTrack);
        switch (next?.action) {
          case "keep":
            return undefined;
          case "container":
            return [where];
          case "drop":
            return noSelector;
          case "start":
            return scopeSelector(selector, where);
          default:
            return isNested(selector) ? undefined : scopeSelector(selector, where);
        }
      },
    },
  });
  if (upcoming.length > 0) throw new Error(lostTrack);
  return code.toString();
}
