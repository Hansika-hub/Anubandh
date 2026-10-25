import { useCallback, useEffect, useRef, useState } from "react";
import { ApiError } from "../api/client";
import { useSession } from "./session";

export interface Loaded<T> {
  data: T | null;
  error: ApiError | null;
  loading: boolean;
  reload: () => void;
}

/**
 * Runs `fn` on mount, whenever `deps` change, and after every live chain event (session.version),
 * so a page always shows the read model's latest state without a manual refresh.
 */
export function useLoad<T>(fn: () => Promise<T>, deps: unknown[], opts: { live?: boolean } = {}): Loaded<T> {
  const { version } = useSession();
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState<ApiError | null>(null);
  const [loading, setLoading] = useState(true);
  const [nonce, setNonce] = useState(0);
  const fnRef = useRef(fn);
  fnRef.current = fn;
  const liveVersion = opts.live === false ? 0 : version;

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    fnRef
      .current()
      .then((d) => {
        if (!cancelled) {
          setData(d);
          setError(null);
        }
      })
      .catch((e: unknown) => {
        if (!cancelled) setError(e instanceof ApiError ? e : new ApiError(0, "ERR_INTERNAL", String(e)));
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [...deps, liveVersion, nonce]);

  const reload = useCallback(() => setNonce((n) => n + 1), []);
  return { data, error, loading, reload };
}

/** Wraps a write: shows a toast, refreshes data, and returns whether it succeeded. */
export function useAction() {
  const { notify, bump } = useSession();
  const [busy, setBusy] = useState<string | null>(null);
  const run = useCallback(
    async (key: string, okText: string, fn: () => Promise<unknown>): Promise<boolean> => {
      setBusy(key);
      try {
        await fn();
        notify("ok", okText);
        bump();
        return true;
      } catch (e) {
        const err = e instanceof ApiError ? e : new ApiError(0, "ERR_INTERNAL", String(e));
        notify("error", `${err.code}: ${err.message}`);
        return false;
      } finally {
        setBusy(null);
      }
    },
    [notify, bump],
  );
  return { busy, run };
}
