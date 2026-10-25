import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import type { Deal } from "../api/types";
import { DealTable } from "../components/DealTable";

const deal: Deal = {
  dealId: "DL-20261020-0001", poNumber: "PO-4411", buyerId: "BUYER-001", supplierId: "SUP-007", status: "PARTIALLY_ACCEPTED",
  currency: "INR", lienAmountPaise: 100_000_000, lienRef: "LIEN-9921", totalReleasedPaise: 80_000_000, totalHeldPaise: 20_000_000,
  mse: { category: "SMALL", termDays: 45, applies: true }, deliveryDeadline: "2026-12-20T00:00:00Z",
  invoice: { irn: null, verified: false }, createdAt: "", updatedAt: "",
};

describe("DealTable", () => {
  it("renders a deal row with money, status and MSE terms", () => {
    render(
      <MemoryRouter future={{ v7_startTransition: true, v7_relativeSplatPath: true }}>
        <DealTable deals={[deal]} />
      </MemoryRouter>,
    );
    expect(screen.getByRole("link", { name: "DL-20261020-0001" })).toHaveAttribute("href", "/deals/DL-20261020-0001");
    expect(screen.getByText("Partially accepted")).toBeInTheDocument();
    expect(screen.getByText("₹8 L")).toBeInTheDocument();
    expect(screen.getByText("₹2 L")).toBeInTheDocument();
    expect(screen.getByText("SMALL · 45d")).toBeInTheDocument();
  });
  it("shows the empty state", () => {
    render(<DealTable deals={[]} empty="Nothing here" />);
    expect(screen.getByText("Nothing here")).toBeInTheDocument();
  });
});
