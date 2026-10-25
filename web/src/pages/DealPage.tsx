import { Link, useParams } from "react-router-dom";
import { api, gw } from "../api/client";
import type { DealDetail, HeldPortion, HistoryEntry, List, Tranche } from "../api/types";
import { InlineForm } from "../components/InlineForm";
import { describe } from "../components/LiveFeed";
import { Badge, Button, Card, Empty, ErrorNote, Loading, Money, Stat } from "../components/ui";
import { canApprove, isDealSupplier, objectionOpen, releasedShare, requiredRoles, signedRoles } from "../lib/deal";
import { compactRupees, countdown, dateTime, label, rupees, shortHash, toPaise } from "../lib/format";
import { useSession } from "../state/session";
import { useDealEvents } from "../state/useDealEvents";
import { useAction, useLoad } from "../state/useLoad";

export function DealPage() {
  const { dealId = "" } = useParams();
  const { persona } = useSession();
  const deal = useLoad(() => api.get<DealDetail>(gw(`/deals/${dealId}`), persona), [dealId, persona.userId]);
  const events = useDealEvents(dealId, persona.org);
  const d = deal.data;

  return (
    <div className="page">
      <div className="crumbs">
        <Link to={persona.home}>← back</Link>
      </div>
      <ErrorNote error={deal.error} />
      <Loading show={deal.loading && !d} />
      {d && (
        <>
          <DealHeader d={d} />
          <LienPanel d={d} />
          <Tranches d={d} />
          <HeldPortions d={d} />
          <Lines d={d} />
          <div className="grid two">
            <Card title="Event timeline">
              {events.length === 0 ? (
                <Empty>No chain events for this deal yet.</Empty>
              ) : (
                <ol className="timeline">
                  {events.map((ev) => (
                    <li key={ev.id}>
                      <span className="tl-name">{ev.eventName}</span>
                      <span className="tl-desc">{describe(ev)}</span>
                      <span className="tl-meta" title={ev.txId}>
                        {dateTime(ev.ts)} · block {ev.blockNo} · {shortHash(ev.txId, 8)}
                      </span>
                    </li>
                  ))}
                </ol>
              )}
            </Card>
            <History dealId={d.dealId} />
          </div>
        </>
      )}
    </div>
  );
}

function DealHeader({ d }: { d: DealDetail }) {
  const share = releasedShare(d);
  return (
    <Card
      title={
        <>
          <span className="mono">{d.dealId}</span> <Badge value={d.status} />
        </>
      }
      actions={<span className="sub">PO {d.poNumber}</span>}
    >
      <div className="stats">
        <Stat label="Lien (locked funds)" value={<Money paise={d.lienAmountPaise} compact />} hint={d.lienRef ?? "awaiting bank"} />
        <Stat label="Released" value={<Money paise={d.totalReleasedPaise} compact />} hint={`${Math.round(share * 100)}% of the lien`} tone="good" />
        <Stat label="Held" value={<Money paise={d.totalHeldPaise} compact />} hint="rejected units awaiting an exit" tone={d.totalHeldPaise > 0 ? "warn" : ""} />
        <Stat
          label="MSME guard"
          value={d.mse.applies ? `${d.mse.category} · ${d.mse.termDays} days` : "Not covered"}
          hint={d.mse.applies ? `${d.mse.udyamNo ?? ""}${d.mse.writtenAgreement ? " · written agreement" : ""}` : "MSMED Act §15 does not apply"}
        />
      </div>
      <div className="progress" aria-label="released share of the lien">
        <span style={{ width: `${share * 100}%` }} />
      </div>
      <dl className="facts">
        <dt>Buyer</dt>
        <dd>{d.buyerId}</dd>
        <dt>Supplier</dt>
        <dd>{d.supplierId}</dd>
        <dt>Deliver by</dt>
        <dd>{dateTime(d.deliveryDeadline)}</dd>
        <dt>Invoice</dt>
        <dd>{d.invoice?.irn ? `${d.invoice.irn} ${d.invoice.verified ? "· verified on GST" : ""}` : "not yet verified"}</dd>
        <dt>Approval policy</dt>
        <dd>
          {d.approvalPolicy.map((b) => `from ${compactRupees(b.minPaise)}: ${b.requiredRoles.join(" + ")}`).join(" · ")}
        </dd>
      </dl>
    </Card>
  );
}

function LienPanel({ d }: { d: DealDetail }) {
  const { persona } = useSession();
  const { busy, run } = useAction();
  if (d.status !== "CREATED") return null;
  return (
    <Card title="Waiting for the bank lien">
      <p>
        No goods can be received until the bank marks a lien of <strong>{rupees(d.lienAmountPaise)}</strong> on the buyer's account.
      </p>
      {persona.role === "BANK_OFFICER" ? (
        <InlineForm
          label="Confirm lien"
          kind="primary"
          busy={busy === "lien"}
          fields={[
            { name: "lienRef", label: "Lien reference", initial: `LIEN-${d.dealId.slice(-4)}` },
            { name: "amount", label: "Amount (₹)", initial: String(d.lienAmountPaise / 100), type: "number", hint: "must equal the PO value" },
          ]}
          onSubmit={(v) =>
            run("lien", "Lien confirmed on chain", () =>
              api.post(gw(`/deals/${d.dealId}/lien`), persona, { lienRef: v.lienRef, amountPaise: toPaise(v.amount ?? "") ?? 0 }),
            )
          }
        />
      ) : (
        <p className="sub">Switch to the Bank Officer to confirm it.</p>
      )}
    </Card>
  );
}

function TrancheActions({ d, t }: { d: DealDetail; t: Tranche }) {
  const { persona } = useSession();
  const { busy, run } = useAction();
  const base = `/deals/${d.dealId}/tranches/${t.trancheNo}`;
  const supplier = isDealSupplier(persona, d);
  const buttons = [];
  if (canApprove(persona, d, t)) {
    buttons.push(
      <Button
        key="approve"
        kind="primary"
        busy={busy === "approve"}
        onClick={() => run("approve", `${persona.role} approved tranche ${t.trancheNo}`, () => api.post(gw(`${base}/approve`), persona))}
      >
        Approve as {label(persona.role)}
      </Button>,
    );
  }
  if (persona.org === "BUYER" && t.status === "APPROVED") {
    buttons.push(
      <Button key="release" kind="primary" busy={busy === "release"} onClick={() => run("release", "Release submitted", () => api.post(gw(`${base}/release`), persona))}>
        Release payment
      </Button>,
    );
  }
  if (supplier && t.status === "DELIVERED") {
    buttons.push(
      <Button key="ack" busy={busy === "ack"} onClick={() => run("ack", "Delivery acknowledged", () => api.post(gw(`${base}/ack`), persona))}>
        Acknowledge delivery
      </Button>,
    );
  }
  if (persona.org === "BUYER" && d.mse.applies && t.status === "DELIVERED" && !objectionOpen(t)) {
    buttons.push(
      <InlineForm
        key="objection"
        label="Raise written objection"
        kind="danger"
        busy={busy === "objection"}
        fields={[{ name: "reason", label: "Reason", initial: "Dimensions out of tolerance" }]}
        onSubmit={(v) => run("objection", "Objection recorded; the MSE clock waits", () => api.post(gw(`${base}/objection`), persona, { reason: v.reason }))}
      />,
    );
  }
  if ((persona.org === "BUYER" || supplier) && objectionOpen(t)) {
    buttons.push(
      <Button key="resolve" busy={busy === "resolve"} onClick={() => run("resolve", "Objection resolution signed", () => api.post(gw(`${base}/objection/resolve`), persona))}>
        Sign objection resolved
      </Button>,
    );
  }
  return buttons.length ? <div className="actions">{buttons}</div> : null;
}

function Tranches({ d }: { d: DealDetail }) {
  if (d.tranches.length === 0) {
    return (
      <Card title="Deliveries">
        <Empty>No goods received yet. The warehouse posts a goods receipt through ingest.</Empty>
      </Card>
    );
  }
  return (
    <Card title="Deliveries (tranches)">
      <div className="tranches">
        {d.tranches.map((t) => {
          const req = requiredRoles(d, t);
          const signed = signedRoles(t);
          const secs = t.mseDeadline ? (new Date(t.mseDeadline).getTime() - Date.now()) / 1000 : null;
          const paid = t.status === "RELEASED" || t.status === "SETTLED";
          return (
            <article key={t.trancheNo} className="tranche">
              <header>
                <h3>Tranche {t.trancheNo}</h3>
                <Badge value={t.status} />
                {t.deemedAcceptance && <Badge value="DEEMED_ACCEPTED" tone="info" />}
              </header>
              <div className="stats compact">
                <Stat label="Accepted value" value={<Money paise={t.acceptedValuePaise} compact />} />
                <Stat label="Held value" value={<Money paise={t.heldValuePaise} compact />} />
                <Stat label="Rail · bank ref" value={t.rail ? `${t.rail} · ${t.bankRef ?? "pending"}` : "—"} />
                {secs !== null && (
                  <Stat
                    label="MSE pay-by"
                    value={paid ? "paid" : countdown(secs)}
                    hint={dateTime(t.mseDeadline)}
                    tone={paid ? "good" : secs < 0 ? "bad" : secs < 5 * 86400 ? "warn" : ""}
                  />
                )}
              </div>
              <table className="table small">
                <thead>
                  <tr>
                    <th>Line</th>
                    <th className="num">Delivered</th>
                    <th className="num">Accepted</th>
                    <th className="num">Rejected</th>
                  </tr>
                </thead>
                <tbody>
                  {t.lines.map((l) => (
                    <tr key={l.lineNo}>
                      <td>{l.lineNo}</td>
                      <td className="num">{l.deliveredQty}</td>
                      <td className="num">{l.acceptedQty ?? "—"}</td>
                      <td className="num">{l.rejectedQty ?? "—"}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
              {req.length > 0 && t.status !== "DELIVERED" && (
                <p className="approvals">
                  Approvals:{" "}
                  {req.map((r) => (
                    <span key={r} className={`chip ${signed.has(r) ? "on" : ""}`}>
                      {signed.has(r) ? "✓ " : ""}
                      {label(r)}
                    </span>
                  ))}
                </p>
              )}
              {objectionOpen(t) && <div className="note warn">Written objection open: {t.objection?.reason}</div>}
              <TrancheActions d={d} t={t} />
            </article>
          );
        })}
      </div>
    </Card>
  );
}

function HeldActions({ d, h }: { d: DealDetail; h: HeldPortion }) {
  const { persona } = useSession();
  const { busy, run } = useAction();
  const base = `/deals/${d.dealId}/held/${h.trancheNo}/${h.lineNo}`;
  const supplier = isDealSupplier(persona, d);
  const party = persona.org === "BUYER" || supplier;
  if (h.state === "CLOSED") return null;
  if (h.state === "DISPUTED" || h.exit === "DISPUTE") {
    return persona.role === "ARBITER" ? (
      <InlineForm
        label="Resolve dispute"
        kind="primary"
        busy={busy === "dres"}
        fields={[
          {
            name: "resolution",
            label: "Resolution",
            options: [
              { value: "RELEASE_TO_SUPPLIER", label: "Release to supplier" },
              { value: "REFUND_TO_BUYER", label: "Refund to buyer" },
            ],
          },
        ]}
        onSubmit={(v) => run("dres", "Dispute resolved", () => api.post(gw(`${base}/dispute/resolve`), persona, { resolution: v.resolution }))}
      />
    ) : (
      <p className="sub">In dispute: the bank's arbiter decides.</p>
    );
  }
  if (!party) return null;
  return (
    <div className="actions">
      {supplier && (
        <InlineForm
          label="Ship replacement"
          kind="primary"
          busy={busy === "repl"}
          fields={[{ name: "qty", label: "Units", initial: String(h.qty), type: "number" }]}
          onSubmit={(v) => run("repl", "Replacement shipped; a new tranche opens", () => api.post(gw(`${base}/replacement`), persona, { qty: Number(v.qty) }))}
        />
      )}
      <InlineForm
        label="Propose credit note"
        busy={busy === "cn"}
        fields={[{ name: "value", label: "Credit (₹)", initial: String(h.valuePaise / 100), type: "number" }]}
        onSubmit={(v) => run("cn", "Credit note proposed", () => api.post(gw(`${base}/credit-note`), persona, { valuePaise: toPaise(v.value ?? "") ?? 0 }))}
      />
      <Button busy={busy === "cns"} onClick={() => run("cns", "Credit note countersigned", () => api.post(gw(`${base}/credit-note/sign`), persona))}>
        Sign credit note
      </Button>
      <InlineForm
        label="Propose price adjustment"
        busy={busy === "pa"}
        fields={[{ name: "price", label: "Adjusted unit price (₹)", type: "number", initial: "" }]}
        onSubmit={(v) =>
          run("pa", "Price adjustment proposed", () => api.post(gw(`${base}/price-adjustment`), persona, { adjustedUnitPricePaise: toPaise(v.price ?? "") ?? 0 }))
        }
      />
      <Button busy={busy === "pas"} onClick={() => run("pas", "Price adjustment countersigned", () => api.post(gw(`${base}/price-adjustment/sign`), persona))}>
        Sign price adjustment
      </Button>
      <InlineForm
        label="Raise dispute"
        kind="danger"
        busy={busy === "disp"}
        fields={[{ name: "reason", label: "Reason", initial: "Rejection reason contested" }]}
        onSubmit={(v) => run("disp", "Dispute raised", () => api.post(gw(`${base}/dispute`), persona, { reason: v.reason }))}
      />
    </div>
  );
}

function HeldPortions({ d }: { d: DealDetail }) {
  if (d.heldPortions.length === 0) return null;
  return (
    <Card title="Held portions">
      <p className="sub">
        Rejected units stay held until one exit is agreed: a replacement, a credit note or a price adjustment (both parties sign), or a dispute the arbiter decides.
      </p>
      <div className="held-list">
        {d.heldPortions.map((h) => (
          <div key={`${h.trancheNo}-${h.lineNo}`} className="held">
            <div className="held-head">
              <strong>
                Tranche {h.trancheNo} · line {h.lineNo}
              </strong>
              <span>
                {h.qty} units · <Money paise={h.valuePaise} compact />
              </span>
              <Badge value={h.state} />
              {h.exit && <Badge value={h.exit} tone="info" />}
            </div>
            <HeldActions d={d} h={h} />
          </div>
        ))}
      </div>
    </Card>
  );
}

function Lines({ d }: { d: DealDetail }) {
  const hidePrice = d.lines.every((l) => l.unitPricePaise == null);
  return (
    <Card title="PO lines (private data collection)">
      <div className="table-wrap">
        <table className="table">
          <thead>
            <tr>
              <th>#</th>
              <th>SKU</th>
              <th className="num">Ordered</th>
              {!hidePrice && <th className="num">Unit price</th>}
              <th className="num">Delivered</th>
              <th className="num">Accepted</th>
              <th className="num">Rejected</th>
              <th className="num">Held</th>
            </tr>
          </thead>
          <tbody>
            {d.lines.map((l) => (
              <tr key={l.lineNo}>
                <td>{l.lineNo}</td>
                <td className="mono">{l.sku}</td>
                <td className="num">{l.orderedQty}</td>
                {!hidePrice && (
                  <td className="num">
                    <Money paise={l.unitPricePaise} />
                  </td>
                )}
                <td className="num">{l.deliveredQty}</td>
                <td className="num">{l.acceptedQty}</td>
                <td className="num">{l.rejectedQty}</td>
                <td className="num">{l.heldQty}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {hidePrice && <p className="sub">Unit prices are hidden from this organisation (private data collection).</p>}
    </Card>
  );
}

function History({ dealId }: { dealId: string }) {
  const { persona } = useSession();
  const h = useLoad(() => api.get<List<HistoryEntry>>(gw(`/deals/${dealId}/history`), persona), [dealId, persona.userId]);
  return (
    <Card title="Ledger history (GetHistoryForKey)">
      <ErrorNote error={h.error} />
      {h.data && h.data.items.length > 0 ? (
        <ol className="timeline">
          {[...h.data.items].reverse().map((e) => (
            <li key={e.txId}>
              <span className="tl-name">{label(e.value?.status ?? (e.isDelete ? "deleted" : "update"))}</span>
              <span className="tl-desc">released {compactRupees(e.value?.totalReleasedPaise ?? 0)}</span>
              <span className="tl-meta mono" title={e.txId}>
                {dateTime(e.timestamp)} · tx {shortHash(e.txId, 16)}
              </span>
            </li>
          ))}
        </ol>
      ) : (
        !h.loading && <Empty>No history.</Empty>
      )}
    </Card>
  );
}
