// Deal rules the UI needs to decide which actions to offer. The chaincode remains the authority:
// these only hide buttons that would certainly fail, never enable something it would reject.
import type { DealDetail, PolicyBand, Role, Tranche } from "../api/types";
import type { Persona } from "./personas";

/** The approval band for an amount: the highest band whose minPaise <= amount (BUILD.md §4.2). */
export function bandFor(policy: PolicyBand[] | undefined, amountPaise: number): PolicyBand | null {
  let best: PolicyBand | null = null;
  for (const b of policy ?? []) {
    if (b.minPaise <= amountPaise && (!best || b.minPaise >= best.minPaise)) best = b;
  }
  return best;
}

export function requiredRoles(deal: DealDetail, t: Tranche): Role[] {
  return bandFor(deal.approvalPolicy, t.acceptedValuePaise)?.requiredRoles ?? [];
}

export function signedRoles(t: Tranche): Set<Role> {
  return new Set((t.approvals ?? []).map((a) => a.role));
}

export const APPROVABLE = new Set(["ACCEPTED", "DEEMED_ACCEPTED"]);

export function canApprove(p: Persona, deal: DealDetail, t: Tranche): boolean {
  return p.org === "BUYER" && APPROVABLE.has(t.status) && requiredRoles(deal, t).includes(p.role) && !signedRoles(t).has(p.role);
}

export function isDealSupplier(p: Persona, deal: { supplierId: string }): boolean {
  return p.org === "SUPPLIER" && p.partyId === deal.supplierId;
}

export function objectionOpen(t: Tranche): boolean {
  const o = t.objection;
  if (!o) return false;
  return o.open ?? !o.resolvedAt;
}

/** Share of the lien already released, 0..1. */
export function releasedShare(d: { lienAmountPaise: number; totalReleasedPaise: number }): number {
  return d.lienAmountPaise > 0 ? Math.min(1, d.totalReleasedPaise / d.lienAmountPaise) : 0;
}
