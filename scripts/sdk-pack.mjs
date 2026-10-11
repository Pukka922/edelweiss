// make sdk-pack: packs the built JS SDK (npm run build:package -w @edelweiss/sdk) into
// dist/edelweiss-sdk-<version>.tgz. The tarball is packed from a staging directory whose package.json points to the
// built JavaScript and type definitions; web/sdk/package.json keeps pointing to the sources, which the workspaces of
// this repository use. The staging directory also gets the LICENSE of the repository, and its package.json the license
// field of web/sdk/package.json.
import { execFileSync } from "node:child_process";
import { cpSync, existsSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import path from "node:path";

const root = path.resolve(import.meta.dirname, "..");
const sdk = path.join(root, "web", "sdk");
const stage = path.join(root, "build", "sdk-pack");
const out = path.join(root, "dist");

const built = [
  "index.js",
  "testing.js",
  "vite.js",
  "extension.css",
  "types/src/index.d.ts",
  "types/src/testing.d.ts",
  "types/vite.d.ts",
];
for (const file of built) {
  if (!existsSync(path.join(sdk, "dist", file))) {
    console.error(
      `sdk-pack: web/sdk/dist/${file} is missing; run npm run build:package -w @edelweiss/sdk first`,
    );
    process.exit(1);
  }
}

const pkg = JSON.parse(readFileSync(path.join(sdk, "package.json"), "utf8"));
// "solid" points to the same files as "default": vite-plugin-solid recognises packages with this condition as Solid
// libraries and lets vitest compile them with the test, so the SDK and the test share one solid-js.
const entry = (types, js) => ({ types, solid: js, default: js });
const manifest = {
  name: pkg.name,
  version: pkg.version,
  description: "SDK for the user interfaces of edelweiss extensions",
  license: pkg.license,
  type: "module",
  exports: {
    ".": entry("./dist/types/src/index.d.ts", "./dist/index.js"),
    "./testing": entry("./dist/types/src/testing.d.ts", "./dist/testing.js"),
    "./vite": { types: "./dist/types/vite.d.ts", default: "./dist/vite.js" },
    "./extension.css": "./dist/extension.css",
  },
  dependencies: pkg.dependencies,
  peerDependencies: pkg.peerDependencies,
};

rmSync(stage, { recursive: true, force: true });
mkdirSync(stage, { recursive: true });
cpSync(path.join(sdk, "dist"), path.join(stage, "dist"), { recursive: true });
cpSync(path.join(root, "LICENSE"), path.join(stage, "LICENSE"));
writeFileSync(path.join(stage, "package.json"), `${JSON.stringify(manifest, null, 2)}\n`);
mkdirSync(out, { recursive: true });
const name = execFileSync("npm", ["pack", "--pack-destination", out], { cwd: stage, encoding: "utf8" })
  .trim()
  .split("\n")
  .pop();
console.log(`sdk-pack: dist/${name}`);
