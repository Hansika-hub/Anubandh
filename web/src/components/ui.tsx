import type { ReactNode } from "react";
import type { ApiError } from "../api/client";
import { compactRupees, label, rupees } from "../lib/format";

const TONE: Record<string, string> = {
  CREATED: "muted",
  FUNDED: "info",
  DELIVERED: "info",
  PENDING: "muted",
  SENT: "info",
  ACCEPTED: "good",
  DEEMED_ACCEPTED: "good",
  PARTIALLY_ACCEPTED: "warn",
  APPROVED: "good",
  RELEASED: "good",
  SETTLED: "strong",
  CONFIRMED: "strong",
  HELD: "warn",
  DISPUTED: "bad",
  FAILED: "bad",
  EXPIRED: "bad",
  CLOSED: "muted",
  PAID_ON_TIME: "strong",
  PAID_LATE: "bad",
  OVERDUE: "bad",
  DUE: "warn",
  CRITICAL: "bad",
  WARN: "warn",
  INFO: "info",
};

export function Badge({ value, tone }: { value: string | null | undefined; tone?: string }) {
  if (!value) return <span className="badge muted">—</span>;
  return <span className={`badge ${tone ?? TONE[value] ?? "muted"}`}>{label(value)}</span>;
}

export function Money({ paise, compact }: { paise: number | null | undefined; compact?: boolean }) {
  return (
    <span className="money" title={rupees(paise)}>
      {compact ? compactRupees(paise) : rupees(paise)}
    </span>
  );
}

export function Card({ title, actions, children, className }: { title?: ReactNode; actions?: ReactNode; children: ReactNode; className?: string }) {
  return (
    <section className={`card ${className ?? ""}`}>
      {(title || actions) && (
        <header className="card-head">
          {title && <h2>{title}</h2>}
          {actions && <div className="card-actions">{actions}</div>}
        </header>
      )}
      {children}
    </section>
  );
}

export function Stat({ label: l, value, hint, tone }: { label: string; value: ReactNode; hint?: ReactNode; tone?: string }) {
  return (
    <div className={`stat ${tone ?? ""}`}>
      <div className="stat-label">{l}</div>
      <div className="stat-value">{value}</div>
      {hint && <div className="stat-hint">{hint}</div>}
    </div>
  );
}

export function ErrorNote({ error }: { error: ApiError | null }) {
  if (!error) return null;
  return (
    <div className="note bad" role="alert">
      <strong>{error.code}</strong> {error.message}
    </div>
  );
}

export function Empty({ children }: { children: ReactNode }) {
  return <div className="empty">{children}</div>;
}

export function Loading({ show }: { show: boolean }) {
  return show ? <div className="loading" aria-live="polite">Loading…</div> : null;
}

export function Button({
  children,
  onClick,
  busy,
  kind = "secondary",
  disabled,
  title,
  type = "button",
}: {
  children: ReactNode;
  onClick?: () => void;
  busy?: boolean;
  kind?: "primary" | "secondary" | "danger" | "ghost";
  disabled?: boolean;
  title?: string;
  type?: "button" | "submit";
}) {
  return (
    <button type={type} className={`btn ${kind}`} onClick={onClick} disabled={disabled || busy} title={title} aria-busy={busy}>
      {busy ? "Working…" : children}
    </button>
  );
}
