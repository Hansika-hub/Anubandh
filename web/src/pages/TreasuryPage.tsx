import { useState } from "react";
import { api, fc, gw } from "../api/client";
import type { Action, Alert, Band, Evaluation, List, MseClock } from "../api/types";
import { BandChart, quantiles } from "../components/BandChart";
import { Badge, Button, Card, Empty, ErrorNote, Loading, Money, Stat } from "../components/ui";
import { compactRupees, dateTime, label, percent } from "../lib/format";
import { useSession } from "../state/session";
import { useAction, useLoad } from "../state/useLoad";

const ORG = "BUYER-001";

export function TreasuryPage() {
  const { persona } = useSession();
  const [horizon, setHorizon] = useState<"14d" | "intraday">("14d");
  const band = useLoad(() => api.get<Band>(fc(`/band?orgId=${ORG}&horizon=${horizon}`), null), [horizon], { live: false });
  const alerts = useLoad(() => api.get<List<Alert>>(fc(`/alerts?orgId=${ORG}&acked=false`), null), [], { live: false });
  const evaluation = useLoad(() => api.get<Evaluation>(fc("/metrics/evaluation"), null), [], { live: false });
  const clocks = useLoad(() => api.get<{ items: MseClock[] }>(gw("/mse/clocks"), persona), [persona.userId]);
  const [selected, setSelected] = useState<string | null>(null);
  const { busy, run } = useAction();

  const pts = band.data?.points.map(quantiles) ?? [];
  const lowest = pts.length ? Math.min(...pts.map((p) => p[0])) : null;
  const mseDue = (clocks.data?.items ?? []).filter((c) => !c.paid);
  const mseDueTotal = mseDue.reduce((a, c) => a + c.amountPaise, 0);
  const active = selected ?? alerts.data?.items[0]?.alertId ?? null;

  return (
    <div className="page">
      <h1>Treasury · liquidity forecast</h1>
      <p className="lede">
        LightGBM release-timing models and Monte Carlo cash paths give a P10–P90 band. The treasurer sees a shortfall before it happens, with ranked
        fixes that never defer an MSE payment.
      </p>
      <div className="stats">
        <Stat label="Median cash, end of horizon" value={pts.length ? compactRupees(pts[pts.length - 1]![1]) : "—"} />
        <Stat
          label="Worst case (P10 low)"
          value={lowest == null ? "—" : compactRupees(lowest)}
          tone={lowest != null && band.data && lowest < band.data.minBufferPaise ? "bad" : ""}
          hint={band.data ? `buffer ${compactRupees(band.data.minBufferPaise)}` : undefined}
        />
        <Stat label="Open shortfall alerts" value={alerts.data?.items.length ?? "…"} tone={alerts.data?.items.length ? "bad" : "good"} />
        <Stat label="MSE payments due (protected)" value={<Money paise={mseDueTotal} compact />} hint={`${mseDue.length} tranches`} />
      </div>
      <Card
        title="Cash position band"
        actions={
          <div className="segmented" role="group" aria-label="Horizon">
            {(["14d", "intraday"] as const).map((h) => (
              <button key={h} className={horizon === h ? "on" : ""} onClick={() => setHorizon(h)}>
                {h === "14d" ? "14 days" : "Intraday"}
              </button>
            ))}
            <Button
              kind="ghost"
              busy={busy === "refresh"}
              onClick={() =>
                run("refresh", "Forecast recomputed", async () => {
                  await api.post(fc(`/refresh?orgId=${ORG}`), null);
                  band.reload();
                  alerts.reload();
                })
              }
            >
              Recompute
            </Button>
          </div>
        }
      >
        <ErrorNote error={band.error} />
        <Loading show={band.loading && !band.data} />
        {band.data && <BandChart band={band.data} />}
      </Card>
      <div className="grid two">
        <Card title="Shortfall alerts">
          <ErrorNote error={alerts.error} />
          {alerts.data && alerts.data.items.length === 0 ? (
            <Empty>No shortfall expected within the horizon: P10 stays above the minimum buffer.</Empty>
          ) : (
            <ul className="alerts">
              {alerts.data?.items.map((a) => (
                <li key={a.alertId} className={a.alertId === active ? "on" : ""}>
                  <button className="alert-pick" onClick={() => setSelected(a.alertId)}>
                    <Badge value={a.severity} /> <strong>{percent(a.shortfallProb)}</strong> chance of breaching the buffer
                    <div className="sub">
                      {dateTime(a.windowStart)} → {dateTime(a.windowEnd)}
                    </div>
                    {a.drivers.map((d) => (
                      <div key={d.feature} className="sub">
                        • {d.text}
                      </div>
                    ))}
                  </button>
                  <Button
                    kind="ghost"
                    busy={busy === `ack-${a.alertId}`}
                    onClick={() => run(`ack-${a.alertId}`, "Alert acknowledged", async () => {
                      await api.post(fc(`/alerts/${a.alertId}/ack`), null);
                      alerts.reload();
                    })}
                  >
                    Acknowledge
                  </Button>
                </li>
              ))}
            </ul>
          )}
        </Card>
        <Card title="Ranked actions">{active ? <Actions alertId={active} /> : <Empty>Select an alert to see the ranked actions.</Empty>}</Card>
      </div>
      <Card title="Model quality (walk-forward, vs moving-average baseline)">
        <ErrorNote error={evaluation.error} />
        <Loading show={evaluation.loading && !evaluation.data} />
        {evaluation.data && (
          <div className="stats">
            <Stat label="MAE, P50 (days)" value={evaluation.data.maeP50.toFixed(2)} hint={`baseline ${evaluation.data.baselineMae.toFixed(2)}`} tone="good" />
            <Stat
              label="Error reduction vs baseline"
              value={percent(1 - evaluation.data.maeP50 / evaluation.data.baselineMae)}
              tone={evaluation.data.maeP50 < evaluation.data.baselineMae ? "good" : "bad"}
            />
            <Stat label="P10–P90 coverage" value={percent(evaluation.data.coverage)} hint="share of outcomes inside the band" />
            <Stat label="Pinball loss" value={evaluation.data.pinballLoss.toFixed(3)} />
          </div>
        )}
      </Card>
    </div>
  );
}

function Actions({ alertId }: { alertId: string }) {
  const acts = useLoad(() => api.get<List<Action>>(fc(`/alerts/${alertId}/actions`), null), [alertId], { live: false });
  return (
    <>
      <ErrorNote error={acts.error} />
      {acts.data && acts.data.items.length === 0 && <Empty>No action needed.</Empty>}
      <ol className="ranked">
        {acts.data?.items
          .slice()
          .sort((a, b) => a.rank - b.rank)
          .map((a) => (
            <li key={a.actionId}>
              <div>
                <strong>{label(a.actionType)}</strong> <Money paise={a.amountPaise} compact />{" "}
                {a.mseSafe ? <Badge value="MSE_SAFE" tone="good" /> : <Badge value="NOT_MSE_SAFE" tone="bad" />}
              </div>
              <div className="sub">
                cost {compactRupees(a.estCostPaise)} · {a.rationale}
              </div>
            </li>
          ))}
      </ol>
      <p className="sub">Actions that would delay a payment to a micro or small supplier are filtered out: MSMED Act §15–16 interest is not an option.</p>
    </>
  );
}
