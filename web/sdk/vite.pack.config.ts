import { defineConfig, type Plugin } from "vite";
import solid from "vite-plugin-solid";

// Builds the files of the SDK tarball that the shell does not use (make sdk-pack): dist/testing.js and dist/vite.js.
// dist/index.js comes from vite.config.ts; the shell vendors that file, so it stays one self-contained module.

/** Test helpers that testing.js bundles; every other SDK module comes from the package entry. */
const testHelpers = new Set(["./test-utils"]);

/**
 * testing.js imports the SDK modules it uses (contexts, createI18n …) from "@edelweiss/sdk", the package entry:
 * TestShell must provide the very contexts that useI18n(), useSession() … of dist/index.js read. A bundled copy of
 * the context modules would give the test its own contexts, and every hook would fail.
 */
const fromPackageEntry: Plugin = {
  name: "edelweiss-sdk-package-entry",
  enforce: "pre",
  resolveId(source, importer) {
    const fromTestFiles = importer !== undefined && /\/src\/(testing\.tsx|test-utils\.ts)$/.test(importer);
    if (!fromTestFiles || !source.startsWith("./") || testHelpers.has(source)) return null;
    return { id: "@edelweiss/sdk", external: true };
  },
};

/** Bare specifiers (packages, node: built-ins) stay imports: they are dependencies of the package. */
const isBare = (id: string) => !id.startsWith(".") && !id.startsWith("/") && !id.startsWith("\0");

export default defineConfig({
  plugins: [fromPackageEntry, solid()],
  build: {
    lib: { entry: { testing: "src/testing.tsx", vite: "vite.ts" }, formats: ["es"] },
    outDir: "dist",
    emptyOutDir: false,
    rolldownOptions: { external: isBare },
  },
});
