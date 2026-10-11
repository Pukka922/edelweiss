import { type Component, createContext, useContext } from "solid-js";

export interface ExtensionContextValue {
  id: string;
  /** Route prefix of the extension in the shell, e.g. "/x/system". */
  basePath: string;
  /** API prefix proxied to the extension backend, e.g. "/ext/system/api". */
  apiBase: string;
}

export const ExtensionContext = createContext<ExtensionContextValue>();

export function useExtension(): ExtensionContextValue {
  const ext = useContext(ExtensionContext);
  if (!ext) throw new Error("useExtension must be used inside an extension");
  return ext;
}

export function extensionContextFor(id: string): ExtensionContextValue {
  return { id, basePath: `/x/${id}`, apiBase: `/ext/${id}/api` };
}

/** What an extension's ui/index.js exports as default. */
export interface ExtensionModule {
  component: Component;
}

export function defineExtension(module: ExtensionModule): ExtensionModule {
  return module;
}

export function isExtensionModule(value: unknown): value is ExtensionModule {
  return (
    typeof value === "object" && value !== null && typeof (value as ExtensionModule).component === "function"
  );
}
