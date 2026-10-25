import { useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { api, gw } from "../api/client";
import type { Deal, DealDetail, List } from "../api/types";
import { Badge, Button, Card, Empty, ErrorNote } from "../components/ui";
import { useSession } from "../state/session";
import { useAction, useLoad } from "../state/useLoad";

interface EmitResult {
  idemKey: string;
  sent: number;
  accepted: number;
  responses: { status: number; body: unknown }[];
}

const RECEIVABLE = ["FUNDED", "DELIVERED", "PARTIALLY_ACCEPTED", "ACCEPTED", "RELEASED"];

/**
 * The warehouse system as the demo plays it: goods receipts and QC results leave through sim-grn
 * as signed webhooks (optionally duplicated and shuffled), pass ingest's queue and idempotent
 * workers, and only then reach the chain. Nothing here calls the gateway's write API directly.
 */
export function WarehousePage() {
  const { persona } = useSession();
  const deals = useLoad(() => api.get<List<Deal>>(gw("/deals?limit=200"), persona), [persona.userId]);
  const candidates = (deals.data?.items ?? []).filter((d) => RECEIVABLE.includes(d.status));
  const [dealId, setDealId] = useState("");
  const chosen = dealId || candidates[0]?.dealId || "";
  const detail = useLoad(() => (chosen ? api.get<DealDetail>(gw(`/deals/${chosen}`), persona) : Promise.resolve(null)), [chosen, persona.userId]);
  const [log, setLog] = useState<(EmitResult & { kind: string; at: string })[]>([]);

  return (
    <div className="page">
      <h1>Warehouse · goods receipt and quality check</h1>
      <p className="lede">
        Events go out as signed webhooks through <code>sim-grn → ingest → Kafka → workers → gateway → chain</code>. Send them twice and shuffled: the
        idempotency keys guarantee one effect.
      </p>
      <ErrorNote error={deals.error} />
      {candidates.length === 0 ? (
        <Card>
          <Empty>No funded deals. Create one as Procurement and confirm its lien as the Bank Officer first.</Empty>
        </Card>
      ) : (
        <Card
          title="Deal"
          actions={
            <select aria-label="Deal" value={chosen} onChange={(e) => setDealId(e.target.value)}>
              {candidates.map((d) => (
                <option key={d.dealId} value={d.dealId}>
                  {d.dealId} · {d.supplierId} · {d.status.toLowerCase()}
                </option>
              ))}
            </select>
          }
        >
          {detail.data && <ReceiptForms d={detail.data} onResult={(r) => setLog((l) => [r, ...l].slice(0, 20))} />}
        </Card>
      )}
      <Card title="Webhook log">
        {log.length === 0 ? (
          <Empty>Nothing sent yet.</Empty>
        ) : (
          <table className="table small">
            <thead>
              <tr>
                <th>When</th>
                <th>Event</th>
                <th>Idempotency key</th>
                <th className="num">Copies sent</th>
                <th className="num">Accepted by ingest</th>
              </tr>
            </thead>
            <tbody>
              {log.map((r, i) => (
                <tr key={i}>
                  <td>{r.at}</td>
                  <td>{r.kind}</td>
                  <td className="mono">{r.idemKey}</td>
                  <td className="num">{r.sent}</td>
                  <td className="num">{r.accepted}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
        <p className="sub">Ingest accepts every signed copy (HTTP 202); the worker applies the first and drops the rest as duplicates.</p>
      </Card>
    </div>
  );
}

function ReceiptForms({ d, onResult }: { d: DealDetail; onResult: (r: EmitResult & { kind: string; at: string }) => void }) {
  const { busy, run } = useAction();
  const nextTranche = d.tranches.find((t) => t.status === "PENDING")?.trancheNo ?? d.tranches.length + 1;
  const toQc = d.tranches.find((t) => t.status === "DELIVERED");
  const [tranche, setTranche] = useState(String(nextTranche));
  const [qty, setQty] = useState<Record<number, string>>({});
  const [acc, setAcc] = useState<Record<number, string>>({});
  const [dupes, setDupes] = useState(1);
  const [shuffle, setShuffle] = useState(true);
  const [irn, setIrn] = useState(`IRN-${d.poNumber}-0001`);

  const pending = d.tranches.find((t) => t.trancheNo === Number(tranche) && t.status === "PENDING");
  const owed = (lineNo: number) => {
    const ln = d.lines.find((l) => l.lineNo === lineNo);
    if (pending) return pending.lines.find((l) => l.lineNo === lineNo)?.deliveredQty ?? 0;
    return ln ? Math.max(0, ln.orderedQty - ln.deliveredQty) : 0;
  };
  const stamp = () => new Date().toLocaleTimeString("en-IN", { hour12: false });

  const sendGrn = async (e: FormEvent) => {
    e.preventDefault();
    const lines = d.lines
      .map((l) => ({ lineNo: l.lineNo, deliveredQty: Number(qty[l.lineNo] ?? owed(l.lineNo)) }))
      .filter((l) => l.deliveredQty > 0);
    await run("grn", "Goods receipt sent to ingest", async () => {
      const r = await api.post<EmitResult>("/sim/grn/emit", null, { dealId: d.dealId, trancheNo: Number(tranche), lines, dupes, shuffle });
      onResult({ ...r, kind: `GRN tranche ${tranche}`, at: stamp() });
    });
  };
  const sendQc = async (e: FormEvent) => {
    e.preventDefault();
    if (!toQc) return;
    const results = toQc.lines.map((l) => {
      const a = Math.min(l.deliveredQty, Math.max(0, Number(acc[l.lineNo] ?? l.deliveredQty)));
      return { lineNo: l.lineNo, acceptedQty: a, rejectedQty: l.deliveredQty - a };
    });
    await run("qc", "Quality check sent to ingest", async () => {
      const r = await api.post<EmitResult>("/sim/qc/emit", null, { dealId: d.dealId, trancheNo: toQc.trancheNo, results, dupes, shuffle });
      onResult({ ...r, kind: `QC tranche ${toQc.trancheNo}`, at: stamp() });
    });
  };
  const sendInvoice = async () => {
    await run("inv", "GST invoice check sent to ingest", async () => {
      const r = await api.post<{ idemKey: string }>("/sim/invoice/emit", null, { dealId: d.dealId, irn });
      onResult({ idemKey: r.idemKey, sent: 1, accepted: 1, responses: [], kind: "GST invoice", at: stamp() });
    });
  };

  return (
    <div className="grid two">
      <form className="form" onSubmit={sendGrn}>
        <h3>Goods receipt (GRN)</h3>
        <label>
          <span>Tranche</span>
          <input type="number" min={1} value={tranche} onChange={(e) => setTranche(e.target.value)} />
          <small>{pending ? "replacement tranche: deliver exactly the shipped units" : "next delivery batch"}</small>
        </label>
        {d.lines.map((l) => (
          <label key={l.lineNo}>
            <span>
              Line {l.lineNo} · {l.sku} delivered
            </span>
            <input type="number" min={0} value={qty[l.lineNo] ?? String(owed(l.lineNo))} onChange={(e) => setQty({ ...qty, [l.lineNo]: e.target.value })} />
          </label>
        ))}
        <div className="row">
          <label>
            <span>Extra duplicate copies</span>
            <input type="number" min={0} max={50} value={dupes} onChange={(e) => setDupes(Number(e.target.value))} />
          </label>
          <label className="check">
            <input type="checkbox" checked={shuffle} onChange={(e) => setShuffle(e.target.checked)} />
            <span>Send concurrently, shuffled</span>
          </label>
        </div>
        <Button type="submit" kind="primary" busy={busy === "grn"}>
          Send GRN
        </Button>
      </form>
      <div>
        <form className="form" onSubmit={sendQc}>
          <h3>Quality check</h3>
          {toQc ? (
            <>
              <p className="sub">
                Tranche {toQc.trancheNo} <Badge value={toQc.status} />
              </p>
              {toQc.lines.map((l) => (
                <label key={l.lineNo}>
                  <span>
                    Line {l.lineNo}: accepted of {l.deliveredQty}
                  </span>
                  <input
                    type="number"
                    min={0}
                    max={l.deliveredQty}
                    value={acc[l.lineNo] ?? String(Math.round(l.deliveredQty * 0.8))}
                    onChange={(e) => setAcc({ ...acc, [l.lineNo]: e.target.value })}
                  />
                </label>
              ))}
              <Button type="submit" kind="primary" busy={busy === "qc"}>
                Send QC
              </Button>
            </>
          ) : (
            <Empty>No delivered tranche waiting for QC.</Empty>
          )}
        </form>
        <div className="form">
          <h3>GST e-invoice</h3>
          <label>
            <span>IRN</span>
            <input value={irn} onChange={(e) => setIrn(e.target.value)} />
            <small>sim-gst checks it, then sends the INVOICE event. IRNs starting with BAD are rejected.</small>
          </label>
          <Button busy={busy === "inv"} onClick={sendInvoice}>
            Verify invoice
          </Button>
        </div>
        <p className="sub">
          <Link to={`/deals/${d.dealId}`}>Open {d.dealId}</Link> to watch the result.
        </p>
      </div>
    </div>
  );
}
