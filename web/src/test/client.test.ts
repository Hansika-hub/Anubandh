import { request, toApiError } from "../api/client";
import { personaById } from "../lib/personas";

describe("api client", () => {
  afterEach(() => vi.restoreAllMocks());

  it("parses the shared error shape and FastAPI's wrapper", () => {
    const e = toApiError(409, { error: { code: "ERR_POLICY_NOT_SATISFIED", message: "needs CFO" } });
    expect([e.status, e.code, e.message]).toEqual([409, "ERR_POLICY_NOT_SATISFIED", "needs CFO"]);
    expect(toApiError(400, { detail: { error: { code: "ERR_BAD_REQUEST", message: "bad" } } }).code).toBe("ERR_BAD_REQUEST");
    expect(toApiError(502, "Bad Gateway").message).toBe("Bad Gateway");
  });

  it("sends the persona as X-Org and X-User and throws ApiError on failure", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ error: { code: "ERR_FORBIDDEN_ROLE", message: "no" } }), { status: 403 }),
    );
    vi.stubGlobal("fetch", fetchMock);
    await expect(request("POST", "/api/v1/deals", personaById("buyer-cfo"), { a: 1 })).rejects.toMatchObject({ code: "ERR_FORBIDDEN_ROLE", status: 403 });
    const init = fetchMock.mock.calls[0]?.[1] as RequestInit;
    expect(init.headers).toMatchObject({ "X-Org": "BUYER", "X-User": "buyer-cfo", "Content-Type": "application/json" });
    expect(init.body).toBe('{"a":1}');
  });

  it("returns parsed JSON on success", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response('{"items":[],"total":0}', { status: 200 })));
    await expect(request("GET", "/api/v1/deals", null)).resolves.toEqual({ items: [], total: 0 });
  });
});
