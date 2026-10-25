import { useEffect, useState, type FormEvent } from "react";
import { useNavigate } from "react-router-dom";
import { api, gw } from "../api/client";
import type { Deal, List, MSE } from "../api/types";
import { DealTable } from "../components/DealTable";
import { Badge, Button, Card, ErrorNote, Loading, Money, Stat } from "../components/ui";
import { compactRupees, newDealId, toPaise } from "../lib/format";
import { useSession } from "../state/session";
import { useAction, useLoad } from "../state/useLoad";

const STATUSES = ["", "CREATED", "FUNDED", "DELIVERED", "PARTIALLY_ACCEPTED", "ACCEPTED", "RELEASED", "SETTLED", "DISPUTED", "EXPIRED"];

export function BuyerPage() {
  const { persona } = useSession();
  const [status, setStatus] = useState("");
  const deals = useLoad(
    () => api.get<List<Deal>>(gw(`/deals?limit=200${status ? `&status=${status}` : ""}`), persona),
    [status, persona.userId],
  );
  const items = deals.data?.items ?? [];
  const sum = (f: (d: Deal) => number) => items.reduce((a, d) => a + f(d), 0);
  const open = items.filter((d) => !["SETTLED", "CLOSED", "EXPIRED"].includes(d.status));

  return (
    <div className="page">
      <h1>Buyer · purchase deals</h1>
      <p className="lede">
        Every purchase order is a Smart Payment Deal: the bank locks the PO value, and money moves line by line as the warehouse verifies the goods.
      </p>
      <div className="stats">
        <Stat label="Open deals" value={open.length} />
        <Stat label="Locked under lien" value={<Money paise={sum((d) => (d.lienRef ? d.lienAmountPaise - d.totalReleasedPaise : 0))} compact />} />
        <Stat label="Released to suppliers" value={<Money paise={sum((d) => d.totalReleasedPaise)} compact />} tone="good" />
        <Stat label="Held (rejected goods)" value={<Money paise={sum((d) => d.totalHeldPaise)} compact />} tone="warn" />
      </div>
      {persona.org === "BUYER" && persona.role === "PROCUREMENT" ? (
        <CreateDeal onCreated={deals.reload} />
      ) : (
        persona.org === "BUYER" && <p className="note info">Switch to Procurement to create deals. Approvers sign on each deal's page.</p>
      )}
      <Card
        title="Deals"
        actions={
          <select aria-label="Filter by status" value={status} onChange={(e) => setStatus(e.target.value)}>
            {STATUSES.map((s) => (
              <option key={s} value={s}>
                {s ? s.replace("_", " ").toLowerCase() : "all statuses"}
              </option>
            ))}
          </select>
        }
      >
        <ErrorNote error={deals.error} />
        <Loading show={deals.loading && !deals.data} />
        {deals.data && <DealTable deals={items} empty="No deals match. Create one above." />}
      </Card>
    </div>
  );
}

interface LineDraft {
  sku: string;
  qty: string;
  price: string;
}

interface Registration {
  udyamNo: string;
  category: MSE["category"];
  msmedApplies: boolean;
  enterpriseName: string;
}

const SUPPLIERS = Array.from({ length: 50 }, (_, i) => `SUP-${String(i + 1).padStart(3, "0")}`);

function CreateDeal({ onCreated }: { onCreated: () => void }) {
  const { persona } = useSession();
  const navigate = useNavigate();
  const { busy, run } = useAction();
  const [open, setOpen] = useState(false);
  const [supplierId, setSupplierId] = useState("SUP-007");
  const [poNumber, setPoNumber] = useState("PO-4411");
  const [reg, setReg] = useState<Registration | null>(null);
  const [regNote, setRegNote] = useState("");
  const [termDays, setTermDays] = useState(45);
  const [agreement, setAgreement] = useState(true);
  const [deadlineDays, setDeadlineDays] = useState(60);
  const [lines, setLines] = useState<LineDraft[]>([{ sku: "SKU-STEEL-12", qty: "100", price: "10000" }]);

  // Udyam lookup (sim-udyam) decides whether the MSMED Act payment guard applies.
  useEffect(() => {
    if (!open) return;
    let cancelled = false;
    setRegNote("checking Udyam registration…");
    api
      .get<Registration>(`/udyam/v1/by-supplier/${supplierId}`, null)
      .then((r) => {
        if (cancelled) return;
        setReg(r);
        setRegNote(`${r.enterpriseName}: ${r.category}${r.msmedApplies ? ", MSMED Act applies" : ", MSMED Act does not apply"}`);
      })
      .catch((e: Error) => {
        if (cancelled) return;
        setReg(null);
        setRegNote(`no Udyam registration (${e.message}); the deal is not MSE-guarded`);
      });
    return () => {
      cancelled = true;
    };
  }, [supplierId, open]);

  const total = lines.reduce((a, l) => a + (Number(l.qty) || 0) * (toPaise(l.price) ?? 0), 0);
  const applies = !!reg?.msmedApplies;

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    const dealId = newDealId();
    const mse: MSE = applies
      ? { category: reg!.category, termDays, udyamNo: reg!.udyamNo, applies: true, writtenAgreement: termDays === 45 && agreement }
      : { category: reg?.category ?? "NONE", termDays: 0, udyamNo: reg?.udyamNo ?? null, applies: false };
    const body = {
      dealId,
      poNumber,
      supplierId,
      deliveryDeadline: new Date(Date.now() + deadlineDays * 86400_000).toISOString().replace(/\.\d{3}Z$/, "Z"),
      mse,
      lines: lines.map((l, i) => ({ lineNo: i + 1, sku: l.sku.trim(), orderedQty: Number(l.qty), unitPricePaise: toPaise(l.price) ?? 0 })),
    };
    const ok = await run("create", `Deal ${dealId} created on chain`, () => api.post(gw("/deals"), persona, body));
    if (ok) {
      setOpen(false);
      onCreated();
      navigate(`/deals/${dealId}`);
    }
  };

  if (!open) {
    return (
      <div className="actions">
        <Button kind="primary" onClick={() => setOpen(true)}>
          New deal
        </Button>
      </div>
    );
  }
  return (
    <Card title="New Smart Payment Deal">
      <form className="form" onSubmit={submit}>
        <div className="form-grid">
          <label>
            <span>Supplier</span>
            <select value={supplierId} onChange={(e) => setSupplierId(e.target.value)}>
              {SUPPLIERS.map((s) => (
                <option key={s}>{s}</option>
              ))}
            </select>
            <small>{regNote}</small>
          </label>
          <label>
            <span>PO number</span>
            <input value={poNumber} onChange={(e) => setPoNumber(e.target.value)} required maxLength={64} />
          </label>
          <label>
            <span>Payment term</span>
            <select value={termDays} onChange={(e) => setTermDays(Number(e.target.value))} disabled={!applies}>
              <option value={15}>15 days (no written agreement)</option>
              <option value={45}>45 days (written agreement, statutory maximum)</option>
            </select>
          </label>
          <label className="check">
            <input type="checkbox" checked={agreement} onChange={(e) => setAgreement(e.target.checked)} disabled={!applies || termDays !== 45} />
            <span>Written agreement on file (MSMED Act §15)</span>
          </label>
          <label>
            <span>Deliver within (days)</span>
            <input type="number" min={1} max={365} value={deadlineDays} onChange={(e) => setDeadlineDays(Number(e.target.value))} />
          </label>
        </div>
        <table className="table small">
          <thead>
            <tr>
              <th>SKU</th>
              <th className="num">Qty</th>
              <th className="num">Unit price (₹)</th>
              <th className="num">Value</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {lines.map((l, i) => (
              <tr key={i}>
                <td>
                  <input aria-label={`line ${i + 1} sku`} value={l.sku} onChange={(e) => setLines(lines.map((x, j) => (j === i ? { ...x, sku: e.target.value } : x)))} required />
                </td>
                <td className="num">
                  <input aria-label={`line ${i + 1} quantity`} type="number" min={1} value={l.qty} onChange={(e) => setLines(lines.map((x, j) => (j === i ? { ...x, qty: e.target.value } : x)))} required />
                </td>
                <td className="num">
                  <input aria-label={`line ${i + 1} unit price`} inputMode="decimal" value={l.price} onChange={(e) => setLines(lines.map((x, j) => (j === i ? { ...x, price: e.target.value } : x)))} required />
                </td>
                <td className="num">{compactRupees((Number(l.qty) || 0) * (toPaise(l.price) ?? 0))}</td>
                <td>
                  {lines.length > 1 && (
                    <Button kind="ghost" onClick={() => setLines(lines.filter((_, j) => j !== i))}>
                      Remove
                    </Button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
        <div className="form-foot">
          <Button kind="ghost" onClick={() => setLines([...lines, { sku: "", qty: "1", price: "" }])}>
            Add line
          </Button>
          <span>
            PO value and lien <strong>{compactRupees(total)}</strong> {applies ? <Badge value={`${reg?.category}`} tone="info" /> : null}
          </span>
          <Button kind="ghost" onClick={() => setOpen(false)}>
            Cancel
          </Button>
          <Button type="submit" kind="primary" busy={busy === "create"} disabled={total <= 0}>
            Create deal
          </Button>
        </div>
      </form>
    </Card>
  );
}
