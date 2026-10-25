import { Link } from "react-router-dom";
import type { Deal } from "../api/types";
import { date } from "../lib/format";
import { Badge, Empty, Money } from "./ui";

export function DealTable({ deals, empty = "No deals yet." }: { deals: Deal[]; empty?: string }) {
  if (deals.length === 0) return <Empty>{empty}</Empty>;
  return (
    <div className="table-wrap">
      <table className="table">
        <thead>
          <tr>
            <th>Deal</th>
            <th>Supplier</th>
            <th>Status</th>
            <th className="num">Lien</th>
            <th className="num">Released</th>
            <th className="num">Held</th>
            <th>MSE</th>
            <th>Deliver by</th>
          </tr>
        </thead>
        <tbody>
          {deals.map((d) => (
            <tr key={d.dealId}>
              <td>
                <Link to={`/deals/${d.dealId}`} className="mono">
                  {d.dealId}
                </Link>
                <div className="sub">{d.poNumber}</div>
              </td>
              <td>{d.supplierId}</td>
              <td>
                <Badge value={d.status} />
              </td>
              <td className="num">
                <Money paise={d.lienAmountPaise} compact />
              </td>
              <td className="num">
                <Money paise={d.totalReleasedPaise} compact />
              </td>
              <td className="num">
                <Money paise={d.totalHeldPaise} compact />
              </td>
              <td>{d.mse?.applies ? `${d.mse.category} · ${d.mse.termDays}d` : <span className="sub">not covered</span>}</td>
              <td>{date(d.deliveryDeadline)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
