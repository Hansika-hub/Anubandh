import { useState } from "react";
import { Link } from "react-router-dom";
import { api, gw } from "../api/client";
import type { ComplianceRow } from "../api/types";
import { Badge, Button, Card, Empty, ErrorNote, Loading, Money, Stat } from "../components/ui";
import { date } from "../lib/format";
import { useSession } from "../state/session";
import { useLoad } from "../state/useLoad";

export function AuditorPage() {
  const { persona, notify } = useSession();
  const report = useLoad(() => api.get<{ generatedAt: string; items: ComplianceRow[] }>(gw("/mse/compliance-report"), persona), [persona.userId]);
  const [downloading, setDownloading] = useState(false);
  const rows = report.data?.items ?? [];
  const late = rows.filter((r) => r.onTime === false);

  // The gateway authorises by header, so the CSV is fetched and saved instead of linked.
  const download = async () => {
    setDownloading(true);
    try {
      const res = await fetch(gw("/mse/compliance-report?format=csv"), { headers: { "X-Org": persona.org, "X-User": persona.userId } });
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      const url = URL.createObjectURL(await res.blob());
      const a = document.createElement("a");
      a.href = url;
      a.download = `msme-compliance-${new Date().toISOString().slice(0, 10)}.csv`;
      a.click();
      URL.revokeObjectURL(url);
    } catch (e) {
      notify("error", `CSV download failed: ${(e as Error).message}`);
    } finally {
      setDownloading(false);
    }
  };

  return (
    <div className="page">
      <h1>Auditor · MSME compliance</h1>
      <p className="lede">
        Every acceptance date, deadline and payment comes from the ledger, so the MSME Form-I half-yearly return and the Section 43B(h) tax check are a
        query, not a reconstruction. Line prices stay hidden from the auditor's organisation.
      </p>
      {persona.org !== "AUDITOR" && <p className="note info">The report is an AUDITOR export. Switch to the Auditor to load it.</p>}
      <div className="stats">
        <Stat label="MSE tranches" value={rows.length} />
        <Stat label="Paid on time" value={rows.filter((r) => r.onTime).length} tone="good" />
        <Stat label="Paid late or overdue" value={late.length} tone={late.length ? "bad" : "good"} />
        <Stat label="Deemed acceptances" value={rows.filter((r) => r.deemedAcceptance).length} hint="no written objection within 15 days" />
      </div>
      <Card
        title="Compliance report"
        actions={
          <Button onClick={download} busy={downloading} disabled={persona.org !== "AUDITOR"}>
            Download CSV
          </Button>
        }
      >
        <ErrorNote error={report.error} />
        <Loading show={report.loading && !report.data} />
        {report.data && rows.length === 0 && <Empty>No MSE tranches yet.</Empty>}
        {rows.length > 0 && (
          <div className="table-wrap">
            <table className="table">
              <thead>
                <tr>
                  <th>Deal · tranche</th>
                  <th>Supplier · Udyam</th>
                  <th className="num">Amount</th>
                  <th>Delivered</th>
                  <th>Acceptance (statutory)</th>
                  <th>Pay by</th>
                  <th>Paid</th>
                  <th>Status</th>
                </tr>
              </thead>
              <tbody>
                {rows.map((r) => (
                  <tr key={`${r.dealId}-${r.trancheNo}`} className={r.onTime === false ? "row-bad" : ""}>
                    <td>
                      <Link to={`/deals/${r.dealId}`} className="mono">
                        {r.dealId}
                      </Link>{" "}
                      · {r.trancheNo}
                    </td>
                    <td>
                      {r.supplierId}
                      <div className="sub mono">
                        {r.udyamNo} · {r.mseCategory} · {r.termDays}d
                      </div>
                    </td>
                    <td className="num">
                      <Money paise={r.amountPaise} compact />
                    </td>
                    <td>{date(r.deliveredAt)}</td>
                    <td>
                      {date(r.acceptanceDate)} {r.deemedAcceptance && <Badge value="DEEMED" tone="info" />}
                    </td>
                    <td>{date(r.mseDeadline)}</td>
                    <td>
                      {date(r.paidDate)}
                      {r.daysLate > 0 && <div className="sub">{r.daysLate} days late</div>}
                    </td>
                    <td>
                      <Badge value={r.complianceStatus} />
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
        <p className="sub">Open a deal for its full ledger history: every state change with its transaction id.</p>
      </Card>
    </div>
  );
}
