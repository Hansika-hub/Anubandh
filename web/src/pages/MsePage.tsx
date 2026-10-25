import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { api, gw } from "../api/client";
import type { MseClock } from "../api/types";
import { Badge, Card, Empty, ErrorNote, Loading, Money, Stat } from "../components/ui";
import { countdown, dateTime } from "../lib/format";
import { useSession } from "../state/session";
import { useLoad } from "../state/useLoad";

type Filter = "open" | "risk" | "all";

export function MsePage() {
  const { persona } = useSession();
  const [riskDays, setRiskDays] = useState(5);
  const [filter, setFilter] = useState<Filter>("open");
  const clocks = useLoad(() => api.get<{ asOf: string; items: MseClock[] }>(gw(`/mse/clocks?riskDays=${riskDays}`), persona), [riskDays, persona.userId]);

  // Tick locally between reloads. secondsLeft is computed against the chain clock (asOf), so the
  // countdown keeps working when the demo has fast-forwarded the mock chain's clock.
  const [, setTick] = useState(0);
  const [loadedAt, setLoadedAt] = useState(Date.now());
  useEffect(() => setLoadedAt(Date.now()), [clocks.data]);
  useEffect(() => {
    const t = window.setInterval(() => setTick((n) => n + 1), 1000);
    return () => window.clearInterval(t);
  }, []);
  const elapsed = (Date.now() - loadedAt) / 1000;

  const all = clocks.data?.items ?? [];
  const items = all.filter((c) => (filter === "all" ? true : filter === "risk" ? !c.paid && (c.atRisk || c.overdue) : !c.paid));
  const unpaid = all.filter((c) => !c.paid);

  return (
    <div className="page">
      <h1>MSE payment guard</h1>
      <p className="lede">
        MSMED Act §15: a micro or small supplier must be paid within the agreed term (45 days at most, 15 without a written agreement) from the day of
        acceptance. Silence for 15 days after delivery is deemed acceptance, and the chain starts the clock itself.
      </p>
      <div className="stats">
        <Stat label="Clocks running" value={unpaid.length} />
        <Stat label="At risk (≤ risk window)" value={unpaid.filter((c) => c.atRisk && !c.overdue).length} tone="warn" />
        <Stat label="Overdue" value={unpaid.filter((c) => c.overdue).length} tone={unpaid.some((c) => c.overdue) ? "bad" : "good"} />
        <Stat label="Paid on time" value={all.filter((c) => c.paid).length} tone="good" hint={clocks.data ? `chain time ${dateTime(clocks.data.asOf)}` : undefined} />
      </div>
      <Card
        title="Clocks"
        actions={
          <div className="row">
            <div className="segmented" role="group" aria-label="Filter">
              {(["open", "risk", "all"] as Filter[]).map((f) => (
                <button key={f} className={filter === f ? "on" : ""} onClick={() => setFilter(f)}>
                  {f === "open" ? "Unpaid" : f === "risk" ? "At risk" : "All"}
                </button>
              ))}
            </div>
            <label className="inline">
              risk window
              <select value={riskDays} onChange={(e) => setRiskDays(Number(e.target.value))}>
                {[3, 5, 7, 10].map((d) => (
                  <option key={d} value={d}>
                    {d} days
                  </option>
                ))}
              </select>
            </label>
          </div>
        }
      >
        <ErrorNote error={clocks.error} />
        <Loading show={clocks.loading && !clocks.data} />
        {clocks.data && items.length === 0 && <Empty>No tranches in this view.</Empty>}
        <div className="clocks">
          {items.map((c) => {
            const left = c.secondsLeft - (c.paid ? 0 : elapsed);
            const pct = c.termDays > 0 ? Math.max(0, Math.min(1, 1 - left / (c.termDays * 86400))) : 1;
            const tone = c.paid ? "good" : left < 0 ? "bad" : c.atRisk ? "warn" : "";
            return (
              <article key={`${c.dealId}-${c.trancheNo}`} className={`clock ${tone}`}>
                <header>
                  <Link to={`/deals/${c.dealId}`} className="mono">
                    {c.dealId}
                  </Link>
                  <span>· tranche {c.trancheNo}</span>
                  <Badge value={c.paid ? "PAID" : c.status} tone={c.paid ? "strong" : undefined} />
                </header>
                <div className="clock-time">{c.paid ? "paid" : countdown(left)}</div>
                <div className="progress">
                  <span style={{ width: `${pct * 100}%` }} />
                </div>
                <dl className="facts small">
                  <dt>Supplier</dt>
                  <dd>
                    {c.supplierId} · {c.mseCategory} · {c.termDays} days
                  </dd>
                  <dt>Amount</dt>
                  <dd>
                    <Money paise={c.amountPaise} compact />
                  </dd>
                  <dt>Accepted</dt>
                  <dd>
                    {dateTime(c.acceptedAt)} {c.deemedAcceptance && "(deemed)"}
                  </dd>
                  <dt>Pay by</dt>
                  <dd>{dateTime(c.mseDeadline)}</dd>
                  {c.paid && (
                    <>
                      <dt>Paid</dt>
                      <dd>{dateTime(c.paidAt)}</dd>
                    </>
                  )}
                </dl>
              </article>
            );
          })}
        </div>
      </Card>
    </div>
  );
}
