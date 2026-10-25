import { Link } from "react-router-dom";
import { api, fc, gw } from "../api/client";
import type { Deal, Health, List } from "../api/types";
import { Badge, Card, Money, Stat } from "../components/ui";
import { PERSONAS } from "../lib/personas";
import { useSession } from "../state/session";
import { useLoad } from "../state/useLoad";

const STEPS = [
  { who: "Procurement", what: "creates the ₹10L deal with SUP-007 (SMALL, 45-day written agreement)", to: "/buyer" },
  { who: "Bank Officer", what: "confirms the lien: the PO value is locked", to: "/bank" },
  { who: "Warehouse", what: "posts the GRN for 100 units, twice and shuffled: one effect", to: "/warehouse" },
  { who: "Warehouse", what: "QC: 80 accepted, 20 rejected → ₹8L accepted, ₹2L held", to: "/warehouse" },
  { who: "Plant Head", what: "approves ₹8L → auto release over NEFT → bank settles", to: "/buyer" },
  { who: "SUP-007", what: "ships 20 replacements → new tranche → the last ₹2L is released", to: "/supplier" },
  { who: "MSE guard", what: "the 15-day MICRO deal is deemed accepted; its clock runs", to: "/mse" },
  { who: "Treasurer", what: "sees the cash band and MSE-safe actions", to: "/treasury" },
];

export function HomePage() {
  const { persona, setPersona } = useSession();
  const health = useLoad(() => api.get<Health>(gw("/health"), null), []);
  const fhealth = useLoad(() => api.get<{ status: string }>(fc("/health"), null), [], { live: false });
  const deals = useLoad(() => api.get<List<Deal>>(gw("/deals?limit=200"), persona), [persona.userId]);
  const items = deals.data?.items ?? [];
  const h = health.data;

  return (
    <div className="page">
      <section className="hero">
        <h1>Money moves only when the goods do.</h1>
        <p className="lede">
          Anubandh turns each B2B purchase order into a Smart Payment Deal on Drunix: the bank locks the value, the warehouse's goods receipt and quality
          check release it line by line, and the MSMED Act payment clock is enforced on chain.
        </p>
      </section>
      <div className="stats">
        <Stat label="Deals" value={deals.data ? items.length : "…"} />
        <Stat label="Released to suppliers" value={<Money paise={items.reduce((a, d) => a + d.totalReleasedPaise, 0)} compact />} tone="good" />
        <Stat label="Held pending an exit" value={<Money paise={items.reduce((a, d) => a + d.totalHeldPaise, 0)} compact />} tone="warn" />
        <Stat label="Payments confirmed by the bank" value={h ? (h.outbox.CONFIRMED ?? 0) : "…"} hint={h ? `${(h.outbox.PENDING ?? 0) + (h.outbox.SENT ?? 0)} in flight` : undefined} />
      </div>
      <div className="grid two">
        <Card title="System">
          <ul className="health">
            <li>
              Gateway <Badge value={h ? "ok" : health.error ? "down" : "…"} tone={h ? "good" : "bad"} />
              {h && (
                <span className="sub">
                  chain: {h.chain.mode === "mock" ? "mock chaincode (demo fallback)" : `Drunix · ${h.chain.channel}/${h.chain.chaincode}`} · block {h.listener.blockNo} ·{" "}
                  {h.listener.processed} events projected
                </span>
              )}
            </li>
            <li>
              Read model <Badge value={h?.db ?? "…"} tone={h?.db === "ok" ? "good" : "bad"} />
            </li>
            <li>
              Forecast <Badge value={fhealth.data ? "ok" : fhealth.error ? "down" : "…"} tone={fhealth.data ? "good" : "bad"} />
            </li>
          </ul>
        </Card>
        <Card title="Act as">
          <div className="personas">
            {PERSONAS.map((p) => (
              <Link key={p.userId} to={p.home} className={`persona-card ${p.userId === persona.userId ? "on" : ""}`} onClick={() => setPersona(p.userId)}>
                <strong>{p.name}</strong>
                <span>
                  {p.org.toLowerCase()} · {p.role.replace("_", " ").toLowerCase()}
                </span>
              </Link>
            ))}
          </div>
        </Card>
      </div>
      <Card title="Demo scenario">
        <ol className="steps">
          {STEPS.map((s, i) => (
            <li key={i}>
              <Link to={s.to}>
                <strong>{s.who}</strong> {s.what}
              </Link>
            </li>
          ))}
        </ol>
      </Card>
    </div>
  );
}
