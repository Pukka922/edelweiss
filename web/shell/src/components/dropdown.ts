import { onCleanup, onMount } from "solid-js";

/** The menus daisyUI builds from <details class="dropdown">; a <details> stays open until someone closes it. */
const OPEN = "details.dropdown[open]";

function openDropdowns(): HTMLDetailsElement[] {
  return [...document.querySelectorAll<HTMLDetailsElement>(OPEN)];
}

/** Closes the dropdown that contains el (for the handler of a menu item). Does nothing for an element outside one. */
export function closeDropdown(el: Element | null | undefined): void {
  const menu = el?.closest<HTMLDetailsElement>("details.dropdown");
  if (menu) menu.open = false;
}

/**
 * Makes the dropdowns of the page behave like menus. Call it once, in a component that lives as long as they do (the
 * navbar). A <details> alone stays open until its summary is clicked again; this closes every open dropdown
 * - on a pointerdown outside of it (which also makes two menus exclusive: opening one closes the other),
 * - when focus moves outside of it (the same for the keyboard),
 * - on Escape, putting the focus back on its summary if it was inside.
 * Changing a <select> inside a dropdown closes nothing: the menu stays for the next setting.
 */
export function useDropdownDismiss(): void {
  const closeOutside = (e: Event) => {
    const target = e.target as Node | null;
    for (const menu of openDropdowns()) if (!target || !menu.contains(target)) menu.open = false;
  };
  const onKeyDown = (e: KeyboardEvent) => {
    if (e.key !== "Escape") return;
    for (const menu of openDropdowns()) {
      const hadFocus = menu.contains(document.activeElement);
      menu.open = false;
      if (hadFocus) menu.querySelector("summary")?.focus();
    }
  };
  onMount(() => {
    document.addEventListener("pointerdown", closeOutside);
    document.addEventListener("focusin", closeOutside);
    document.addEventListener("keydown", onKeyDown);
  });
  onCleanup(() => {
    document.removeEventListener("pointerdown", closeOutside);
    document.removeEventListener("focusin", closeOutside);
    document.removeEventListener("keydown", onKeyDown);
  });
}
