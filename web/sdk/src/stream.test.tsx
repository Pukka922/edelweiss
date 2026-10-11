import { render } from "@solidjs/testing-library";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ExtensionContext, extensionContextFor } from "./extension";
import { SessionContext } from "./session";
import { useStream } from "./stream";
import { FakeEventSource, fakeSession } from "./test-utils";

beforeEach(() => {
  FakeEventSource.instances = [];
  vi.stubGlobal("EventSource", FakeEventSource);
});
afterEach(() => vi.unstubAllGlobals());

describe("useStream", () => {
  it("opens the extension stream, tracks status and parses named events", () => {
    function Probe() {
      const stream = useStream<{ cpu: number }>("/metrics", { event: "metrics" });
      return (
        <p>
          {stream.status()}:{stream.data()?.cpu ?? "-"}
        </p>
      );
    }
    const { container, unmount } = render(() => (
      <ExtensionContext.Provider value={extensionContextFor("system")}>
        <Probe />
      </ExtensionContext.Provider>
    ));
    const es = FakeEventSource.instances[0]!;
    expect(es.url).toBe("/ext/system/api/metrics");
    expect(container.textContent).toBe("connecting:-");
    es.open();
    es.emit("metrics", JSON.stringify({ cpu: 42 }));
    expect(container.textContent).toBe("open:42");
    es.emit("metrics", "not json");
    expect(container.textContent).toBe("open:42");
    es.fail(true);
    expect(container.textContent).toBe("error:42");
    unmount();
    expect(es.readyState).toBe(FakeEventSource.CLOSED);
  });

  it("asks the session when the browser gives up on the stream", () => {
    const session = fakeSession();
    function Probe() {
      useStream("/metrics", { event: "metrics" });
      return null;
    }
    const { unmount } = render(() => (
      <SessionContext.Provider value={session.api}>
        <ExtensionContext.Provider value={extensionContextFor("system")}>
          <Probe />
        </ExtensionContext.Provider>
      </SessionContext.Provider>
    ));
    const es = FakeEventSource.instances[0]!;
    es.fail(false); // reconnecting: the browser keeps trying, nothing to ask
    expect(session.calls.refresh).toBe(0);
    es.fail(true); // closed for good: maybe the session is gone
    expect(session.calls.refresh).toBe(1);
    unmount();
  });
});
