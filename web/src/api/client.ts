// Thin fetch wrapper. Every call goes to the same origin: nginx (or the Vite dev proxy) forwards
// /api to the gateway, /forecast to the forecast service and /sim/* to the simulators.
import type { Persona } from "../lib/personas";

export class ApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
    readonly details?: unknown,
  ) {
    super(message);
    this.name = "ApiError";
  }
}

/** Parses the shared error shape {"error": {code, message, details}} (BUILD.md §12). */
export function toApiError(status: number, body: unknown): ApiError {
  const err = (body as { error?: { code?: string; message?: string; details?: unknown } } | null)?.error;
  if (err && typeof err === "object") {
    return new ApiError(status, err.code ?? `HTTP_${status}`, err.message ?? `HTTP ${status}`, err.details);
  }
  // FastAPI wraps HTTPException details as {"detail": {...}}
  const detail = (body as { detail?: unknown } | null)?.detail;
  if (detail && typeof detail === "object" && "error" in (detail as object)) {
    return toApiError(status, detail);
  }
  return new ApiError(status, `HTTP_${status}`, typeof body === "string" && body ? body : `HTTP ${status}`);
}

export async function request<T>(method: string, path: string, persona: Persona | null, body?: unknown): Promise<T> {
  const headers: Record<string, string> = { Accept: "application/json" };
  if (persona) {
    headers["X-Org"] = persona.org;
    headers["X-User"] = persona.userId;
  }
  if (body !== undefined) headers["Content-Type"] = "application/json";
  let res: Response;
  try {
    res = await fetch(path, { method, headers, body: body === undefined ? undefined : JSON.stringify(body) });
  } catch (e) {
    throw new ApiError(0, "ERR_NETWORK", `cannot reach ${path.split("/")[1]}: ${(e as Error).message}`);
  }
  const text = await res.text();
  let parsed: unknown = text;
  try {
    parsed = text ? JSON.parse(text) : null;
  } catch {
    /* plain text body */
  }
  if (!res.ok) throw toApiError(res.status, parsed);
  return parsed as T;
}

export const api = {
  get: <T>(path: string, persona: Persona | null) => request<T>("GET", path, persona),
  post: <T>(path: string, persona: Persona | null, body?: unknown) => request<T>("POST", path, persona, body ?? {}),
};

export const gw = (path: string) => `/api/v1${path}`;
export const fc = (path: string) => `/forecast/v1${path}`;
