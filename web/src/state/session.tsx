import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import type { ChainEvent } from "../api/types";
import { DEFAULT_PERSONA, personaById, type Persona } from "../lib/personas";

const STORE_KEY = "anubandh.persona";
const FEED_MAX = 80;

interface Toast {
  id: number;
  kind: "ok" | "error";
  text: string;
}

interface Session {
  persona: Persona;
  setPersona: (userId: string) => void;
  /** Live chain events, newest first. */
  feed: ChainEvent[];
  connected: boolean;
  /** Increments on every chain event; pages reload their data when it changes. */
  version: number;
  bump: () => void;
  toasts: Toast[];
  notify: (kind: Toast["kind"], text: string) => void;
  dismiss: (id: number) => void;
}

const Ctx = createContext<Session | null>(null);

function readStored(): Persona {
  try {
    return personaById(localStorage.getItem(STORE_KEY));
  } catch {
    return DEFAULT_PERSONA;
  }
}

export function SessionProvider({ children, live = true }: { children: ReactNode; live?: boolean }) {
  const [persona, setP] = useState<Persona>(readStored);
  const [feed, setFeed] = useState<ChainEvent[]>([]);
  const [connected, setConnected] = useState(false);
  const [version, setVersion] = useState(0);
  const [toasts, setToasts] = useState<Toast[]>([]);
  const toastId = useRef(0);

  const setPersona = useCallback((userId: string) => {
    const p = personaById(userId);
    setP(p);
    try {
      localStorage.setItem(STORE_KEY, p.userId);
    } catch {
      /* storage unavailable: keep the choice for this tab only */
    }
  }, []);

  const bump = useCallback(() => setVersion((v) => v + 1), []);

  const dismiss = useCallback((id: number) => setToasts((t) => t.filter((x) => x.id !== id)), []);
  const notify = useCallback(
    (kind: Toast["kind"], text: string) => {
      const id = ++toastId.current;
      setToasts((t) => [...t.slice(-3), { id, kind, text }]);
      window.setTimeout(() => dismiss(id), kind === "error" ? 9000 : 4500);
    },
    [dismiss],
  );

  // One SSE connection for the whole app. The gateway filters by org (?org=), replays from
  // Last-Event-ID after a reconnect, and EventSource reconnects on its own.
  useEffect(() => {
    if (!live || typeof EventSource === "undefined") return;
    const es = new EventSource(`/api/v1/events/stream?org=${encodeURIComponent(persona.org)}`);
    let debounce: number | undefined;
    const onEvent = (e: MessageEvent<string>) => {
      try {
        const ev = JSON.parse(e.data) as ChainEvent;
        setFeed((f) => (f.some((x) => x.id === ev.id) ? f : [ev, ...f].slice(0, FEED_MAX)));
        window.clearTimeout(debounce);
        debounce = window.setTimeout(bump, 250);
      } catch {
        /* ignore keep-alives and malformed frames */
      }
    };
    es.onopen = () => setConnected(true);
    es.onerror = () => setConnected(false);
    es.onmessage = onEvent;
    // The gateway names each SSE event after the chain event; listen to all of them.
    const names = EVENT_NAMES;
    names.forEach((n) => es.addEventListener(n, onEvent as EventListener));
    return () => {
      window.clearTimeout(debounce);
      names.forEach((n) => es.removeEventListener(n, onEvent as EventListener));
      es.close();
      setConnected(false);
      setFeed([]);
    };
  }, [persona.org, live, bump]);

  const value = useMemo(
    () => ({ persona, setPersona, feed, connected, version, bump, toasts, notify, dismiss }),
    [persona, setPersona, feed, connected, version, bump, toasts, notify, dismiss],
  );
  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}

export function useSession(): Session {
  const s = useContext(Ctx);
  if (!s) throw new Error("useSession outside SessionProvider");
  return s;
}

// Chain event names, as the gateway publishes them (gateway/internal/model/events.go). The
// gateway unwraps TxEventBatch, so each logical event arrives as its own SSE message.
export const EVENT_NAMES = [
  "DealCreated",
  "LienConfirmed",
  "InvoiceVerified",
  "GoodsReceived",
  "DeliveryAcknowledged",
  "QualityRecorded",
  "HeldPortionCreated",
  "TrancheAccepted",
  "DeemedAcceptance",
  "MseClockStarted",
  "MseClockRestarted",
  "MseDeadlineApproaching",
  "ApprovalRecorded",
  "TrancheReleased",
  "SettlementConfirmed",
  "ReplacementShipped",
  "CreditNoteProposed",
  "CreditNoteSigned",
  "PriceAdjustmentProposed",
  "PriceAdjustmentSigned",
  "ObjectionRaised",
  "ObjectionResolved",
  "DisputeRaised",
  "DisputeResolved",
  "DealExpired",
];
