import { Link } from "react-router-dom";
import { compactRupees, dateTime, shortHash } from "../lib/format";
import { useSession } from "../state/session";
import type { ChainEvent } from "../api/types";

const ICON: Record<string, string> = {
  DealCreated: "＋",
  LienConfirmed: "🔒",
  GoodsReceived: "📦",
  QualityRecorded: "🔍",
  TrancheAccepted: "✓",
  DeemedAcceptance: "⏱",
  HeldPortionCreated: "⏸",
  ApprovalRecorded: "✍",
  TrancheReleased: "₹",
  SettlementConfirmed: "🏦",
  MseClockStarted: "⏳",
  MseDeadlineApproaching: "⚠",
  DisputeRaised: "⚑",
};

/** One-line summary of an event's payload. */
export function describe(ev: ChainEvent): string {
  const p = (ev.payload ?? {}) as Record<string, unknown>;
  const num = (k: string) => (typeof p[k] === "number" ? (p[k] as number) : undefined);
  const parts: string[] = [];
  if (ev.trancheNo) parts.push(`tranche ${ev.trancheNo}`);
  const amount = num("amountPaise") ?? num("acceptedValuePaise") ?? num("valuePaise") ?? num("lienAmountPaise");
  if (amount !== undefined) parts.push(compactRupees(amount));
  if (typeof p.role === "string") parts.push(p.role);
  if (typeof p.rail === "string") parts.push(p.rail);
  if (typeof p.bankRef === "string") parts.push(p.bankRef);
  if (typeof p.daysLeft === "number") parts.push(`${p.daysLeft} days left`);
  return parts.join(" · ");
}

export function LiveFeed() {
  const { feed, connected } = useSession();
  return (
    <aside className="feed" aria-label="Live chain events">
      <header className="feed-head">
        <h2>Live ledger</h2>
        <span className={`dot ${connected ? "on" : "off"}`} title={connected ? "connected to the gateway event stream" : "reconnecting"} />
      </header>
      {feed.length === 0 ? (
        <p className="feed-empty">{connected ? "Waiting for chain events…" : "Connecting to the gateway…"}</p>
      ) : (
        <ol className="feed-list">
          {feed.map((ev) => (
            <li key={ev.id} className="feed-item">
              <span className="feed-icon" aria-hidden>
                {ICON[ev.eventName] ?? "•"}
              </span>
              <div className="feed-body">
                <div className="feed-title">{ev.eventName}</div>
                <div className="feed-meta">
                  <Link to={`/deals/${ev.dealId}`}>{ev.dealId}</Link> {describe(ev) && <>· {describe(ev)}</>}
                </div>
                <div className="feed-tx" title={ev.txId}>
                  block {ev.blockNo} · tx {shortHash(ev.txId, 8)} · {dateTime(ev.ts)}
                </div>
              </div>
            </li>
          ))}
        </ol>
      )}
    </aside>
  );
}
