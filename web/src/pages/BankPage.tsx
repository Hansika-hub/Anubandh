import { Link } from "react-router-dom";
import { api, gw } from "../api/client";
import type { Deal, List, OutboxRow } from "../api/types";
import { DealTable } from "../components/DealTable";
import { Badge, Card, Empty, ErrorNote, Money, Stat } from "../components/ui";
import { dateTime } from "../lib/format";
import { useSession } from "../state/session";
import { useLoad } from "../state/useLoad";

export function BankPage() {
  const { persona } = useSession();
  const canSeeOutbox = persona.org === "BANK" || persona.org === "BUYER"; // gateway: outbox is BANK/BUYER only
  const created = useLoad(() => api.get<List<Deal>>(gw("/deals?status=CREATED&limit=200"), persona), [persona.userId]);
  const disputed = useLoad(() => api.get<List<Deal>>(gw("/deals?status=DISPUTED&limit=200"), persona), [persona.userId]);
  const outbox = useLoad(
    () => (canSeeOutbox ? api.get<List<OutboxRow>>(gw("/payments/outbox?limit=200"), persona) : Promise.resolve({ items: [] as OutboxRow[] })),
    [persona.userId, canSeeOutbox],
  );
  const rows = outbox.data?.items ?? [];
  const count = (s: string) => rows.filter((r) => r.status === s).length;

  return (
    <div className="page">
      <h1>Bank · liens, rails and disputes</h1>
      <p className="lede">
        The bank locks each PO's value as a lien, pays released tranches over the cheapest rail that fits, and its arbiter settles disputes on held units.
      </p>
      <div className="stats">
        <Stat label="Liens to confirm" value={created.data?.items.length ?? "…"} />
        <Stat label="Payments pending" value={count("PENDING") + count("SENT")} />
        <Stat label="Confirmed on chain" value={count("CONFIRMED")} tone="good" />
        <Stat label="Failed (operator action)" value={count("FAILED")} tone={count("FAILED") ? "bad" : ""} />
      </div>
      <Card title="Deals waiting for a lien">
        <ErrorNote error={created.error} />
        {created.data && <DealTable deals={created.data.items} empty="No deal is waiting for a lien." />}
        <p className="sub">Open a deal as the Bank Officer to confirm its lien.</p>
      </Card>
      <Card title="Payment outbox">
        <ErrorNote error={outbox.error} />
        {!canSeeOutbox ? (
          <Empty>The payment outbox is visible to the bank and the buyer only.</Empty>
        ) : rows.length === 0 ? (
          <Empty>No payments yet. A release puts one here in the same database transaction as the chain event.</Empty>
        ) : (
          <div className="table-wrap">
            <table className="table">
              <thead>
                <tr>
                  <th>Deal · tranche</th>
                  <th className="num">Amount</th>
                  <th>Rail</th>
                  <th>Status</th>
                  <th>Bank ref (UTR)</th>
                  <th className="num">Attempts</th>
                  <th>Updated</th>
                </tr>
              </thead>
              <tbody>
                {rows.map((r) => (
                  <tr key={`${r.dealId}-${r.trancheNo}`}>
                    <td>
                      <Link to={`/deals/${r.dealId}`} className="mono">
                        {r.dealId}
                      </Link>{" "}
                      · {r.trancheNo}
                    </td>
                    <td className="num">
                      <Money paise={r.amountPaise} compact />
                    </td>
                    <td>{r.rail}</td>
                    <td>
                      <Badge value={r.status} />
                      {r.lastError && r.status !== "CONFIRMED" && (
                        <div className="sub" title={r.lastError}>
                          {r.lastError.slice(0, 60)}
                        </div>
                      )}
                    </td>
                    <td className="mono">{r.bankRef ?? "—"}</td>
                    <td className="num">{r.attempts}</td>
                    <td>{dateTime(r.updatedAt)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>
      <Card title="Disputes for the arbiter">
        <ErrorNote error={disputed.error} />
        {disputed.data && <DealTable deals={disputed.data.items} empty="No open disputes." />}
        {persona.role !== "ARBITER" && <p className="sub">Switch to the Arbiter to resolve a dispute on the deal's page.</p>}
      </Card>
    </div>
  );
}
