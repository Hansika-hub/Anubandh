import { Link } from "react-router-dom";
import { api, gw } from "../api/client";
import type { Deal, List, MseClock } from "../api/types";
import { DealTable } from "../components/DealTable";
import { Badge, Card, Empty, ErrorNote, Loading, Money, Stat } from "../components/ui";
import { countdown, dateTime } from "../lib/format";
import { useSession } from "../state/session";
import { useLoad } from "../state/useLoad";

export function SupplierPage() {
  const { persona } = useSession();
  const supplierId = persona.org === "SUPPLIER" ? persona.partyId : undefined;
  const deals = useLoad(
    () => api.get<List<Deal>>(gw(`/deals?limit=200${supplierId ? `&supplierId=${supplierId}` : ""}`), persona),
    [supplierId, persona.userId],
  );
  const clocks = useLoad(() => api.get<{ items: MseClock[] }>(gw(`/mse/clocks${supplierId ? `?supplierId=${supplierId}` : ""}`), persona), [supplierId, persona.userId]);
  const items = deals.data?.items ?? [];
  const owed = items.reduce((a, d) => a + (d.lienRef ? d.lienAmountPaise - d.totalReleasedPaise - d.totalHeldPaise : 0), 0);
  const unpaid = (clocks.data?.items ?? []).filter((c) => !c.paid);

  return (
    <div className="page">
      <h1>Supplier · {supplierId ?? "all suppliers"}</h1>
      <p className="lede">
        Funds are locked before you ship. You see each delivery accepted line by line, money released the same day, and the legal MSME payment clock for every
        tranche.
      </p>
      {!supplierId && <p className="note info">Switch to a supplier user (SUP-007 or SUP-012) to act on deliveries and held portions.</p>}
      <div className="stats">
        <Stat label="Deals" value={items.length} />
        <Stat label="Received" value={<Money paise={items.reduce((a, d) => a + d.totalReleasedPaise, 0)} compact />} tone="good" />
        <Stat label="Held for an exit" value={<Money paise={items.reduce((a, d) => a + d.totalHeldPaise, 0)} compact />} tone="warn" />
        <Stat label="Locked, still to deliver" value={<Money paise={owed} compact />} />
      </div>
      <Card title="Payment clocks (MSMED Act §15)">
        <ErrorNote error={clocks.error} />
        {unpaid.length === 0 ? (
          <Empty>No unpaid MSE tranches.</Empty>
        ) : (
          <table className="table">
            <thead>
              <tr>
                <th>Deal · tranche</th>
                <th>Status</th>
                <th className="num">Amount</th>
                <th>Accepted</th>
                <th>Pay by</th>
                <th>Time left</th>
              </tr>
            </thead>
            <tbody>
              {unpaid.map((c) => (
                <tr key={`${c.dealId}-${c.trancheNo}`} className={c.overdue ? "row-bad" : c.atRisk ? "row-warn" : ""}>
                  <td>
                    <Link to={`/deals/${c.dealId}`} className="mono">
                      {c.dealId}
                    </Link>{" "}
                    · {c.trancheNo}
                  </td>
                  <td>
                    <Badge value={c.status} />
                  </td>
                  <td className="num">
                    <Money paise={c.amountPaise} compact />
                  </td>
                  <td>
                    {dateTime(c.acceptedAt)} {c.deemedAcceptance && <Badge value="DEEMED" tone="info" />}
                  </td>
                  <td>{dateTime(c.mseDeadline)}</td>
                  <td>{countdown(c.secondsLeft)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Card>
      <Card title="My deals">
        <ErrorNote error={deals.error} />
        <Loading show={deals.loading && !deals.data} />
        {deals.data && <DealTable deals={items} empty="No deals with this supplier yet." />}
        <p className="sub">Open a deal to acknowledge deliveries, ship replacements, or propose a credit note or price adjustment for held units.</p>
      </Card>
    </div>
  );
}
