import type { DealDetail, Tranche } from "../api/types";
import { bandFor, canApprove, isDealSupplier, objectionOpen, requiredRoles } from "../lib/deal";
import { personaById } from "../lib/personas";

const policy = [
  { minPaise: 0, requiredRoles: ["PLANT_HEAD" as const] },
  { minPaise: 500_000_000, requiredRoles: ["PLANT_HEAD" as const, "CFO" as const] },
];

function tranche(over: Partial<Tranche>): Tranche {
  return {
    dealId: "DL-20261020-0002", trancheNo: 1, status: "ACCEPTED", lines: [], acceptedValuePaise: 600_000_000, heldValuePaise: 0,
    deliveredAt: null, acceptedAt: null, deemedAcceptance: false, approvals: [], releaseIdemKey: null, bankRef: null, rail: null,
    mseClockStart: null, mseDeadline: null, ...over,
  };
}

const deal = { approvalPolicy: policy, supplierId: "SUP-012" } as unknown as DealDetail;

describe("approval policy", () => {
  it("picks the highest band at or below the amount", () => {
    expect(bandFor(policy, 80_000_000)?.requiredRoles).toEqual(["PLANT_HEAD"]);
    expect(bandFor(policy, 600_000_000)?.requiredRoles).toEqual(["PLANT_HEAD", "CFO"]);
    expect(bandFor([], 1)).toBeNull();
  });
  it("lets each required role approve once, only on an accepted tranche", () => {
    const t = tranche({});
    expect(requiredRoles(deal, t)).toEqual(["PLANT_HEAD", "CFO"]);
    expect(canApprove(personaById("buyer-planthead"), deal, t)).toBe(true);
    expect(canApprove(personaById("buyer-cfo"), deal, t)).toBe(true);
    expect(canApprove(personaById("buyer-treasurer"), deal, t)).toBe(false);
    expect(canApprove(personaById("bank-officer"), deal, t)).toBe(false);
    const signed = tranche({ approvals: [{ role: "PLANT_HEAD", userId: "buyer-planthead", signedAt: "" }] });
    expect(canApprove(personaById("buyer-planthead"), deal, signed)).toBe(false);
    expect(canApprove(personaById("buyer-cfo"), deal, tranche({ status: "DELIVERED" }))).toBe(false);
    expect(canApprove(personaById("buyer-cfo"), deal, tranche({ status: "DEEMED_ACCEPTED" }))).toBe(true);
  });
  it("matches a supplier user to its own deals only", () => {
    expect(isDealSupplier(personaById("supplier-012"), deal)).toBe(true);
    expect(isDealSupplier(personaById("supplier-007"), deal)).toBe(false);
  });
  it("reads both objection shapes", () => {
    expect(objectionOpen(tranche({ objection: { reason: "x", raisedAt: "", open: true } }))).toBe(true);
    expect(objectionOpen(tranche({ objection: { reason: "x", raisedAt: "", resolvedAt: "2026-10-21T00:00:00Z" } }))).toBe(false);
    expect(objectionOpen(tranche({ objection: null }))).toBe(false);
  });
});
