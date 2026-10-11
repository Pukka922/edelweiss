export interface FakeRequest {
  method: string;
  url: string;
  headers: Record<string, string>;
  body: unknown;
}

export type Handler = (req: FakeRequest) => Response | Promise<Response>;

export function json(status: number, body?: unknown): Response {
  return new Response(body === undefined ? null : JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

/** fetch stand-in routing "METHOD /path" to handlers; unknown routes answer 404. */
export function fakeFetch(routes: Record<string, Handler>) {
  const calls: FakeRequest[] = [];
  const impl = async (input: RequestInfo | URL, init: RequestInit = {}): Promise<Response> => {
    const url = typeof input === "string" ? input : input instanceof URL ? input.pathname : input.url;
    const req: FakeRequest = {
      method: init.method ?? "GET",
      url,
      headers: (init.headers ?? {}) as Record<string, string>,
      body: typeof init.body === "string" ? JSON.parse(init.body) : undefined,
    };
    calls.push(req);
    const handler = routes[`${req.method} ${url}`];
    return handler ? handler(req) : json(404, { error: { code: "not_found", message: url } });
  };
  return Object.assign(impl as typeof fetch, { calls });
}
