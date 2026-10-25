import { useEffect, useState } from "react";
import type { ChainEvent, Org } from "../api/types";
import { EVENT_NAMES } from "./session";

/**
 * The full event timeline of one deal. The gateway's stream replays a deal's past events when
 * opened with ?dealId=, then keeps sending new ones, so this needs no separate history call.
 */
export function useDealEvents(dealId: string | undefined, org: Org): ChainEvent[] {
  const [events, setEvents] = useState<ChainEvent[]>([]);
  useEffect(() => {
    setEvents([]);
    if (!dealId || typeof EventSource === "undefined") return;
    const es = new EventSource(`/api/v1/events/stream?org=${encodeURIComponent(org)}&dealId=${encodeURIComponent(dealId)}`);
    const onEvent = (e: MessageEvent<string>) => {
      try {
        const ev = JSON.parse(e.data) as ChainEvent;
        setEvents((list) => (list.some((x) => x.id === ev.id) ? list : [...list, ev].sort((a, b) => a.id - b.id)));
      } catch {
        /* keep-alive */
      }
    };
    es.onmessage = onEvent;
    EVENT_NAMES.forEach((n) => es.addEventListener(n, onEvent as EventListener));
    return () => {
      EVENT_NAMES.forEach((n) => es.removeEventListener(n, onEvent as EventListener));
      es.close();
    };
  }, [dealId, org]);
  return events;
}
