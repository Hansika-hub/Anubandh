// Demo identities. These mirror gateway/config/users.json: the gateway maps X-Org + X-User to an
// enrolled Fabric identity, so the userIds must match that file exactly.
import type { Org, Role } from "../api/types";

export interface Persona {
  userId: string;
  org: Org;
  role: Role;
  partyId?: string;
  name: string;
  home: string;
}

export const PERSONAS: Persona[] = [
  { userId: "buyer-procurement", org: "BUYER", role: "PROCUREMENT", partyId: "BUYER-001", name: "Procurement", home: "/buyer" },
  { userId: "buyer-planthead", org: "BUYER", role: "PLANT_HEAD", partyId: "BUYER-001", name: "Plant Head", home: "/buyer" },
  { userId: "buyer-cfo", org: "BUYER", role: "CFO", partyId: "BUYER-001", name: "CFO", home: "/buyer" },
  { userId: "buyer-treasurer", org: "BUYER", role: "TREASURER", partyId: "BUYER-001", name: "Treasurer", home: "/treasury" },
  { userId: "buyer-warehouse", org: "BUYER", role: "WAREHOUSE", partyId: "BUYER-001", name: "Warehouse Desk", home: "/warehouse" },
  { userId: "supplier-007", org: "SUPPLIER", role: "SUPPLIER_USER", partyId: "SUP-007", name: "SUP-007 Accounts", home: "/supplier" },
  { userId: "supplier-012", org: "SUPPLIER", role: "SUPPLIER_USER", partyId: "SUP-012", name: "SUP-012 Accounts", home: "/supplier" },
  { userId: "bank-officer", org: "BANK", role: "BANK_OFFICER", name: "Bank Officer", home: "/bank" },
  { userId: "bank-arbiter", org: "BANK", role: "ARBITER", name: "Arbiter", home: "/bank" },
  { userId: "auditor", org: "AUDITOR", role: "AUDITOR", name: "Auditor", home: "/auditor" },
];

export const DEFAULT_PERSONA = PERSONAS[0] as Persona;

export function personaById(userId: string | null | undefined): Persona {
  return PERSONAS.find((p) => p.userId === userId) ?? DEFAULT_PERSONA;
}

export const ORG_LABEL: Record<Org, string> = { BUYER: "Buyer", SUPPLIER: "Supplier", BANK: "Bank", AUDITOR: "Auditor" };
