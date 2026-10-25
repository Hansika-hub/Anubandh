// Shapes served by the gateway (gateway/openapi.yaml) and the forecast service (BUILD.md §10.3).
// Money is always integer paise.

export type Org = "BUYER" | "SUPPLIER" | "BANK" | "AUDITOR";
export type Role =
  | "WAREHOUSE"
  | "PLANT_HEAD"
  | "CFO"
  | "PROCUREMENT"
  | "TREASURER"
  | "SUPPLIER_USER"
  | "BANK_OFFICER"
  | "ARBITER"
  | "AUDITOR";

export type DealStatus =
  | "CREATED"
  | "FUNDED"
  | "DELIVERED"
  | "ACCEPTED"
  | "PARTIALLY_ACCEPTED"
  | "RELEASED"
  | "SETTLED"
  | "DISPUTED"
  | "EXPIRED"
  | "CLOSED";

export type TrancheStatus =
  | "PENDING"
  | "DELIVERED"
  | "ACCEPTED"
  | "DEEMED_ACCEPTED"
  | "APPROVED"
  | "RELEASED"
  | "SETTLED"
  | "HELD"
  | "CLOSED";

export interface MSE {
  category: "MICRO" | "SMALL" | "MEDIUM" | "NONE";
  termDays: number;
  udyamNo?: string | null;
  applies: boolean;
  writtenAgreement?: boolean;
}

export interface PolicyBand {
  minPaise: number;
  requiredRoles: Role[];
}

export interface Deal {
  dealId: string;
  poNumber: string;
  buyerId: string;
  supplierId: string;
  status: DealStatus;
  currency: string;
  lienAmountPaise: number;
  lienRef: string | null;
  totalReleasedPaise: number;
  totalHeldPaise: number;
  mse: MSE;
  deliveryDeadline: string;
  invoice: { irn: string | null; verified: boolean };
  createdAt: string;
  updatedAt: string;
}

export interface Line {
  lineNo: number;
  sku: string;
  orderedQty: number;
  unitPricePaise?: number; // hidden from AUDITOR
  deliveredQty: number;
  acceptedQty: number;
  rejectedQty: number;
  heldQty: number;
}

export interface Approval {
  role: Role;
  userId: string;
  signedAt: string;
}

export interface Objection {
  reason: string;
  raisedAt: string;
  resolvedAt?: string | null;
  open?: boolean;
}

export interface Tranche {
  dealId: string;
  trancheNo: number;
  status: TrancheStatus;
  lines: { lineNo: number; deliveredQty: number; acceptedQty?: number; rejectedQty?: number }[];
  acceptedValuePaise: number;
  heldValuePaise: number;
  deliveredAt: string | null;
  acceptedAt: string | null;
  deemedAcceptance: boolean;
  approvals: Approval[] | null;
  releaseIdemKey: string | null;
  bankRef: string | null;
  rail: string | null;
  mseClockStart: string | null;
  mseDeadline: string | null;
  releasedAt?: string | null;
  settledAt?: string | null;
  heldExit?: string | null;
  objection?: Objection | null;
}

export interface HeldPortion {
  dealId: string;
  trancheNo: number;
  lineNo: number;
  qty: number;
  valuePaise: number;
  exit: "REPLACEMENT" | "CREDIT_NOTE" | "PRICE_ADJUSTMENT" | "DISPUTE" | null;
  state: string;
}

export interface DealDetail extends Deal {
  approvalPolicy: PolicyBand[];
  toleranceBps: number;
  lines: Line[];
  tranches: Tranche[];
  heldPortions: HeldPortion[];
}

export interface List<T> {
  items: T[];
  total?: number;
}

export interface HistoryEntry {
  txId: string;
  timestamp: string | null;
  isDelete: boolean;
  value: Partial<Deal> | null;
}

export interface MseClock {
  dealId: string;
  trancheNo: number;
  buyerId: string;
  supplierId: string;
  mseCategory: string;
  termDays: number;
  udyamNo: string | null;
  status: TrancheStatus;
  amountPaise: number;
  deliveredAt: string | null;
  acceptedAt: string | null;
  deemedAcceptance: boolean;
  mseClockStart: string | null;
  mseDeadline: string | null;
  daysLeft: number;
  secondsLeft: number;
  paid: boolean;
  paidAt: string | null;
  atRisk: boolean;
  overdue: boolean;
}

export interface ComplianceRow {
  dealId: string;
  trancheNo: number;
  buyerId: string;
  supplierId: string;
  udyamNo: string | null;
  mseCategory: string;
  termDays: number;
  amountPaise: number;
  deliveredAt: string | null;
  acceptanceDate: string | null;
  deemedAcceptance: boolean;
  mseDeadline: string | null;
  paidDate: string | null;
  onTime: boolean | null;
  daysLate: number;
  complianceStatus: string;
}

export interface OutboxRow {
  dealId: string;
  trancheNo: number;
  amountPaise: number;
  supplierAccountRef: string;
  rail: string;
  status: "PENDING" | "SENT" | "CONFIRMED" | "FAILED";
  bankRef: string | null;
  attempts: number;
  lastError: string | null;
  createdAt: string;
  updatedAt: string;
}

export interface Health {
  status: string;
  chain: { mode: string; channel: string; chaincode: string };
  db: string;
  listener: { running: boolean; blockNo: number; processed: number; lastEventAt: string | null };
  outbox: Record<string, number>;
  now: string;
}

/** One SSE message from GET /api/v1/events/stream. */
export interface ChainEvent {
  id: number;
  eventName: string;
  dealId: string;
  trancheNo?: number | null;
  txId: string;
  blockNo: number;
  ts: string;
  payload: Record<string, unknown> | null;
}

// ---- forecast ----

export interface BandPoint {
  ts: string;
  p10?: number;
  p50?: number;
  p90?: number;
  p10Paise?: number;
  p50Paise?: number;
  p90Paise?: number;
}

export interface Band {
  orgId: string;
  horizon: "14d" | "intraday";
  points: BandPoint[];
  minBufferPaise: number;
}

export interface Driver {
  feature: string;
  impact: number;
  text: string;
}

export interface Alert {
  alertId: string;
  orgId: string;
  severity: "INFO" | "WARN" | "CRITICAL";
  shortfallProb: number;
  windowStart: string;
  windowEnd: string;
  drivers: Driver[];
  acked: boolean;
}

export interface Action {
  actionId: string;
  actionType: "SWEEP" | "DRAW_CREDIT_LINE" | "RESCHEDULE_NON_MSE" | "PARK_SURPLUS" | string;
  amountPaise: number;
  estCostPaise: number;
  rank: number;
  rationale: string;
  mseSafe: boolean;
}

export interface Evaluation {
  pinballLoss: number;
  maeP50: number;
  coverage: number;
  baselineMae: number;
  folds?: unknown[];
}
