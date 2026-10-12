/** The class of the element the shell renders an extension in; its build scopes the extension's style sheet to it. */
export function extensionScopeClass(id: string): string {
  return `edelweiss-ext-${id}`;
}
