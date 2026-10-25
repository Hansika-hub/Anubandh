// Formatting helpers. Money arrives as integer paise; amounts are shown in rupees with Indian
// digit grouping, and compactly in lakh / crore where space is short.

const inr = new Intl.NumberFormat("en-IN", { style: "currency", currency: "INR", maximumFractionDigits: 2 });

export function rupees(paise: number | null | undefined): string {
  if (paise == null || !Number.isFinite(paise)) return "—";
  return inr.format(paise / 100).replace(/\.00$/, "");
}

/** ₹10 L, ₹1.25 Cr, ₹8,000 */
export function compactRupees(paise: number | null | undefined): string {
  if (paise == null || !Number.isFinite(paise)) return "—";
  const r = paise / 100;
  const sign = r < 0 ? "-" : "";
  const a = Math.abs(r);
  const trim = (n: number) => (Math.round(n * 100) / 100).toString();
  if (a >= 1e7) return `${sign}₹${trim(a / 1e7)} Cr`;
  if (a >= 1e5) return `${sign}₹${trim(a / 1e5)} L`;
  return `${sign}₹${Math.round(a).toLocaleString("en-IN")}`;
}

export function dateTime(iso: string | null | undefined): string {
  if (!iso) return "—";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "—";
  return d.toLocaleString("en-IN", { day: "2-digit", month: "short", hour: "2-digit", minute: "2-digit", hour12: false });
}

export function date(iso: string | null | undefined): string {
  if (!iso) return "—";
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? "—" : d.toLocaleDateString("en-IN", { day: "2-digit", month: "short", year: "numeric" });
}

/** "3d 4h", "5h 12m", "overdue 2d" from a number of seconds. */
export function countdown(seconds: number): string {
  const late = seconds < 0;
  let s = Math.abs(Math.trunc(seconds));
  const d = Math.floor(s / 86400);
  s -= d * 86400;
  const h = Math.floor(s / 3600);
  const m = Math.floor((s - h * 3600) / 60);
  const body = d > 0 ? `${d}d ${h}h` : h > 0 ? `${h}h ${m}m` : `${m}m`;
  return late ? `overdue ${body}` : body;
}

export function shortHash(h: string | null | undefined, n = 10): string {
  if (!h) return "—";
  return h.length <= n ? h : `${h.slice(0, n)}…`;
}

export function percent(x: number | null | undefined, digits = 0): string {
  if (x == null || !Number.isFinite(x)) return "—";
  return `${(x * 100).toFixed(digits)}%`;
}

/** Human label for an enum value: PARTIALLY_ACCEPTED -> Partially accepted */
export function label(v: string | null | undefined): string {
  if (!v) return "—";
  const s = v.toLowerCase().replace(/_/g, " ");
  return s.charAt(0).toUpperCase() + s.slice(1);
}

/** A new deal id DL-YYYYMMDD-NNNN (the gateway requires the date to be valid). */
export function newDealId(now = new Date(), rand: () => number = Math.random): string {
  const y = now.getFullYear();
  const m = String(now.getMonth() + 1).padStart(2, "0");
  const d = String(now.getDate()).padStart(2, "0");
  const n = String(Math.floor(rand() * 9000) + 1000);
  return `DL-${y}${m}${d}-${n}`;
}

export function toPaise(rupeesValue: string): number | null {
  const t = rupeesValue.replace(/[,\s₹]/g, "");
  if (!/^\d+(\.\d{1,2})?$/.test(t)) return null;
  return Math.round(Number(t) * 100);
}
