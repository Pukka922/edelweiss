import { cleanup, fireEvent, render } from "@solidjs/testing-library";
import { afterEach, describe, expect, it } from "vitest";
import { closeDropdown, useDropdownDismiss } from "./dropdown";

afterEach(cleanup);

/** Two menus and something outside of them, with the dismiss behavior switched on once. */
function Menus() {
  useDropdownDismiss();
  return (
    <>
      <details class="dropdown" data-testid="a">
        <summary>Menu A</summary>
        <button type="button">Item A</button>
      </details>
      <details class="dropdown" data-testid="b">
        <summary>Menu B</summary>
        <button type="button">Item B</button>
      </details>
      <button type="button">Outside</button>
    </>
  );
}

function setup() {
  const view = render(() => <Menus />);
  const menu = (id: "a" | "b") => view.getByTestId(id) as HTMLDetailsElement;
  const summary = (id: "a" | "b") => view.getByText(`Menu ${id.toUpperCase()}`);
  return { ...view, menu, summary };
}

describe("dropdown dismissal", () => {
  it("closes an open menu on a pointerdown outside of it, not on one inside", () => {
    const view = setup();
    view.menu("a").open = true;

    fireEvent.pointerDown(view.getByText("Item A"));
    expect(view.menu("a").open).toBe(true);
    fireEvent.pointerDown(view.summary("a"));
    expect(view.menu("a").open).toBe(true);

    fireEvent.pointerDown(view.getByText("Outside"));
    expect(view.menu("a").open).toBe(false);
  });

  it("closes the other menu when one is opened", () => {
    const view = setup();
    fireEvent.pointerDown(view.summary("a"));
    fireEvent.click(view.summary("a"));
    expect(view.menu("a").open).toBe(true);

    fireEvent.pointerDown(view.summary("b"));
    fireEvent.click(view.summary("b"));
    expect(view.menu("b").open).toBe(true);
    expect(view.menu("a").open).toBe(false);
  });

  it("closes the other menu when focus moves to it from the keyboard", () => {
    const view = setup();
    view.menu("a").open = true;
    view.summary("a").focus();
    expect(view.menu("a").open).toBe(true);

    view.summary("b").focus();
    expect(view.menu("a").open).toBe(false);
  });

  it("closes a menu when focus leaves it, but not while focus moves inside it", () => {
    const view = setup();
    view.menu("a").open = true;
    view.getByText("Item A").focus();
    expect(view.menu("a").open).toBe(true);

    view.getByText("Outside").focus();
    expect(view.menu("a").open).toBe(false);
  });

  it("closes every open menu on Escape and puts the focus back on the summary", () => {
    const view = setup();
    view.menu("a").open = true;
    view.menu("b").open = true;
    view.getByText("Item A").focus();

    fireEvent.keyDown(document, { key: "Escape" });
    expect(view.menu("a").open).toBe(false);
    expect(view.menu("b").open).toBe(false);
    expect(document.activeElement).toBe(view.summary("a"));
  });

  it("leaves the focus where it is on Escape when it was not inside an open menu", () => {
    const view = setup();
    view.getByText("Outside").focus();
    view.menu("a").open = true; // opened without moving the focus (a script, the mouse)

    fireEvent.keyDown(document, { key: "Escape" });
    expect(view.menu("a").open).toBe(false);
    expect(document.activeElement).toBe(view.getByText("Outside"));
  });

  it("ignores other keys", () => {
    const view = setup();
    view.menu("a").open = true;
    view.getByText("Item A").focus();

    fireEvent.keyDown(document, { key: "Tab" });
    expect(view.menu("a").open).toBe(true);
  });

  it("lets an item close its menu", () => {
    const view = setup();
    view.menu("b").open = true;

    closeDropdown(view.getByText("Item B"));
    expect(view.menu("b").open).toBe(false);
    closeDropdown(view.getByText("Outside")); // not inside a menu: nothing happens
    closeDropdown(null);
  });

  it("stops listening when the component is gone", () => {
    const view = setup();
    const foreign = document.createElement("details");
    foreign.className = "dropdown";
    foreign.open = true;
    document.body.append(foreign);
    view.unmount();

    fireEvent.pointerDown(document.body);
    fireEvent.keyDown(document, { key: "Escape" });
    expect(foreign.open).toBe(true);
    foreign.remove();
  });
});
