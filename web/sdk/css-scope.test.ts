// @vitest-environment node
import { describe, expect, it } from "vitest";
import { scopeCss } from "./css-scope";
import { extensionScopeClass } from "./src/scope";

const w = ":where(.edelweiss-ext-docker)";
const scoped = (css: string) => scopeCss(css, "docker");

describe("extensionScopeClass", () => {
  it("names the container of an extension", () => {
    expect(extensionScopeClass("docker")).toBe("edelweiss-ext-docker");
    expect(extensionScopeClass("my-ext")).toBe("edelweiss-ext-my-ext");
  });
});

describe("scopeCss", () => {
  it("puts the container before a class", () => {
    expect(scoped(".p-4{color:red}")).toBe(`${w} .p-4{color:red}`);
  });
  it("uses the id of the extension", () => {
    expect(scopeCss(".p-4{color:red}", "my-ext")).toBe(":where(.edelweiss-ext-my-ext) .p-4{color:red}");
  });
  it("scopes each selector of a list", () => {
    expect(scoped(".a,.b{color:red}")).toBe(`${w} .a,${w} .b{color:red}`);
  });
  it("keeps Tailwind's escaped and arbitrary selectors", () => {
    expect(scoped(".w-\\[37px\\]{color:red}")).toBe(`${w} .w-\\[37px\\]{color:red}`);
    expect(scoped(".hover\\:bg-base-200:hover{color:red}")).toBe(
      `${w} .hover\\:bg-base-200:hover{color:red}`,
    );
  });
  it("scopes inside @media, @supports, @container, @starting-style and nested @layer", () => {
    const out = scoped(
      "@media (width>=40rem){.sm\\:p-6{color:red}}@supports (display:grid){.g{color:red}}" +
        "@container (width>=10rem){.c{color:red}}@starting-style{.s{color:red}}" +
        "@layer utilities{@layer daisyui.l1{.btn{color:red}}}",
    );
    for (const sel of [".sm\\:p-6", ".g", ".c", ".s", ".btn"])
      expect(out).toContain(`${w} ${sel}{color:red}`);
    expect(out).toContain("@layer utilities{@layer daisyui.l1{");
    expect(out.match(/:where\(/g)).toHaveLength(5);
  });
  it("keeps the rules of the page itself global", () => {
    for (const css of [
      ":root{--x:1}",
      ":root:has(.modal-open){overflow:hidden}",
      ":root:not(span){--y:1}",
      ":root,[data-theme]{--x:1}",
      "[data-theme=light]{--x:1}",
      "html{color:red}",
      "body{margin:0}",
      ":host{color:red}",
    ]) {
      expect(scoped(css)).not.toContain(":where(");
    }
  });
  it("puts the container after a leading page compound", () => {
    expect(scoped("[data-theme=dark] .x{color:red}")).toBe(`[data-theme=dark] ${w} .x{color:red}`);
    expect(scoped("html .x{color:red}")).toBe(`html ${w} .x{color:red}`);
    expect(scoped(":root:has(.modal-open) .x{color:red}")).toBe(`:root:has(.modal-open) ${w} .x{color:red}`);
  });
  it("puts the container behind the last of several leading page compounds", () => {
    expect(scoped("html body .x{color:red}")).toBe(`html body ${w} .x{color:red}`);
    expect(scoped("[data-theme=dark] body .x{color:red}")).toBe(`[data-theme=dark] body ${w} .x{color:red}`);
    expect(scoped(":root>body .x{color:red}")).toBe(`:root>body ${w} .x{color:red}`);
    expect(scoped("html .a body .x{color:red}")).toBe(`html ${w} .a body .x{color:red}`);
  });
  it("keeps a selector of page compounds only global", () => {
    for (const css of [
      "html body{color:red}",
      ":root>body{color:red}",
      "html [data-theme=dark]{color:red}",
    ]) {
      expect(scoped(css)).toBe(css);
    }
  });
  it("puts the variables of @theme on the container instead of :root and :host", () => {
    expect(scoped("@layer theme{:root,:host{--spacing:.3rem}}")).toBe(`@layer theme{${w}{--spacing:.3rem}}`);
    expect(scoped("@layer theme{:host,:root{--spacing:.3rem}}")).toBe(`@layer theme{${w}{--spacing:.3rem}}`);
    expect(scoped("@layer theme{@media (width>=40rem){:root{--x:1}}}")).toBe(
      `@layer theme{@media (width>=40rem){${w}{--x:1}}}`,
    );
  });
  it("scopes the other selectors of the theme layer as usual, and the rules after it", () => {
    expect(
      scoped(
        "@layer theme{:root,:host,.a,:root:has(.b){--x:1}[data-theme=dark]{--y:1}}:root{--z:1}.c{color:red}",
      ),
    ).toBe(
      `@layer theme{${w},${w} .a,:root:has(.b){--x:1}[data-theme=dark]{--y:1}}:root{--z:1}${w} .c{color:red}`,
    );
    expect(scoped("@layer theme{:host(.a){--x:1}}")).toBe("@layer theme{:host(.a){--x:1}}");
  });
  it("leaves :root and :host global outside the theme layer", () => {
    for (const css of [
      ":root{--x:1}",
      ":root,:host{--x:1}",
      "@layer base{:root,:host{--x:1}}",
      "@layer other.theme{:root{--x:1}}",
      "@layer theme.other{:root{--x:1}}",
      "@media (width>=40rem){:root{--x:1}}",
    ]) {
      expect(scoped(css)).not.toContain(":where(");
    }
  });
  it("scopes an @scope rule inside the theme layer by the same rules as anywhere", () => {
    expect(scoped("@layer theme{@scope (.a){:root{--x:1}}}")).toBe(
      `@layer theme{@scope(${w} .a){:root{--x:1}}}`,
    );
  });
  it("lists the selectors of every theme layer, wherever it sits", () => {
    // two theme layers with another layer in between (lightningcss merges them into the first)
    expect(scoped("@layer theme{:root{--a:1}}@layer base{:root{--b:2}}@layer theme{:host{--c:3}}")).toBe(
      `@layer theme{${w}{--a:1;--c:3}}@layer base{:root{--b:2}}`,
    );
    // inside @media, and inside @scope, where the rules are relative to the scope and stay as they are
    expect(scoped("@media (width>=40rem){@layer theme{:root{--x:1}}}")).toBe(
      `@media (width>=40rem){@layer theme{${w}{--x:1}}}`,
    );
    expect(scoped("@scope (.a){@layer theme{:root{--x:1}}}")).toBe(
      `@scope(${w} .a){@layer theme{:root{--x:1}}}`,
    );
    // without :root and :host, and with the rules after it
    expect(scoped("@layer theme{.a{--x:1}}.b{--y:1}")).toBe(`@layer theme{${w} .a{--x:1}}${w} .b{--y:1}`);
  });
  it("passes a @page rule in the theme layer, whose selectors are not selectors", () => {
    expect(scoped("@layer theme{@page :first{margin:0}:root{--x:1}}.a{color:red}")).toBe(
      `@layer theme{@page:first{margin:0}${w}{--x:1}}${w} .a{color:red}`,
    );
  });
  it("leaves @keyframes, @property and @font-face alone", () => {
    const out = scoped(
      '@keyframes spin{0%{opacity:0}to{opacity:1}}@property --tw-x{syntax:"*";inherits:false}' +
        "@font-face{font-family:f;src:url(f.woff2)}",
    );
    expect(out).not.toContain(":where(");
    expect(out).toContain("@keyframes spin");
  });
  it("scopes pseudo-elements and the universal selector", () => {
    expect(scoped(".modal::backdrop{color:red}")).toBe(`${w} .modal::backdrop{color:red}`);
    expect(scoped("*,:before,:after,::backdrop{--a:0}").match(/:where\(/g)).toHaveLength(4);
  });
  it("leaves selectors inside :is, :not and :has as they are", () => {
    expect(scoped(":is(.a .b) .c{color:red}")).toBe(`${w} :is(.a .b) .c{color:red}`);
    expect(scoped(".x:not(.y){color:red}")).toBe(`${w} .x:not(.y){color:red}`);
  });
  // lightningcss unnests for Tailwind's browser targets; the container still comes only from the parent selector
  it("scopes a nested rule only through its parent", () => {
    expect(scoped(".a{color:red;&:hover{color:green}}")).toBe(`${w} .a{color:red}${w} .a:hover{color:green}`);
  });
  it("scopes a nested rule only through its parent, also when at-rules sit in between", () => {
    expect(
      scoped(
        ".a{@media (hover:hover){&:hover{color:red}}.b &{color:red}@container (width>=1rem){color:red}}",
      ),
    ).toBe(
      `@media (hover:hover){${w} .a:hover{color:red}}.b :is(${w} .a){color:red}` +
        `@container (width>=1rem){${w} .a{color:red}}`,
    );
  });
  it("does not scope a selector that takes & into :is()", () => {
    expect(scoped(".a{:is(&) .b{color:red}}")).toBe(`${w} .a .b{color:red}`);
  });
  it("puts the container in front of a selector with several combinators", () => {
    expect(scoped(".a .b>.c{color:red}")).toBe(`${w} .a .b>.c{color:red}`);
    expect(scoped("[data-theme=dark] .a .b{color:red}")).toBe(`[data-theme=dark] ${w} .a .b{color:red}`);
    expect(scoped(".a .b [data-theme=dark]{color:red}")).toBe(".a .b [data-theme=dark]{color:red}");
  });
  it("scopes only the start of an @scope rule", () => {
    expect(scoped("@scope (.card){img{color:red}}")).toBe(`@scope(${w} .card){img{color:red}}`);
    expect(scoped("@scope (.a) to (.b){.c{color:red}}")).toBe(`@scope(${w} .a) to (.b){.c{color:red}}`);
    expect(scoped("@scope (.join){:scope>.x{color:red}}")).toBe(`@scope(${w} .join){:scope>.x{color:red}}`);
  });
  it("scopes the rules around an @scope rule, also when it holds rules of its own kind", () => {
    expect(
      scoped(
        ".a{color:red}@scope (.b){@media (hover:hover){.c{color:red}}@scope (.d){.e{color:red}}}" +
          ".f{color:red}@scope (.g){.h{color:red}}.i{color:red}",
      ),
    ).toBe(
      `${w} .a{color:red}@scope(${w} .b){@media (hover:hover){.c{color:red}}@scope(.d){.e{color:red}}}` +
        `${w} .f{color:red}@scope(${w} .g){.h{color:red}}${w} .i{color:red}`,
    );
  });
  it("keeps the vendor prefixes Tailwind's browser targets need", () => {
    expect(scoped(".a{-webkit-backdrop-filter:blur(4px);backdrop-filter:blur(4px)}")).toBe(
      `${w} .a{-webkit-backdrop-filter:blur(4px);backdrop-filter:blur(4px)}`,
    );
    expect(scoped(".a{-webkit-text-decoration:underline red;text-decoration:underline red}")).toBe(
      `${w} .a{-webkit-text-decoration:underline red;text-decoration:underline red}`,
    );
  });
  it("does not merge rules a browser would drop as a whole", () => {
    expect(scoped(".a{color:red}.b::picker(select){color:red}")).toBe(
      `${w} .a{color:red}${w} .b::picker(select){color:red}`,
    );
  });
  it("fails on CSS it cannot parse", () => {
    expect(() => scoped("..a{color:red}")).toThrow();
  });
});
