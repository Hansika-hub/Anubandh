import { NavLink, Route, Routes, useNavigate } from "react-router-dom";
import { LiveFeed } from "./components/LiveFeed";
import { ORG_LABEL, PERSONAS } from "./lib/personas";
import { AuditorPage } from "./pages/AuditorPage";
import { BankPage } from "./pages/BankPage";
import { BuyerPage } from "./pages/BuyerPage";
import { DealPage } from "./pages/DealPage";
import { HomePage } from "./pages/HomePage";
import { MsePage } from "./pages/MsePage";
import { NotFound } from "./pages/NotFound";
import { SupplierPage } from "./pages/SupplierPage";
import { TreasuryPage } from "./pages/TreasuryPage";
import { WarehousePage } from "./pages/WarehousePage";
import { useSession } from "./state/session";

const NAV = [
  { to: "/", label: "Overview", end: true },
  { to: "/buyer", label: "Buyer" },
  { to: "/warehouse", label: "Warehouse" },
  { to: "/supplier", label: "Supplier" },
  { to: "/bank", label: "Bank" },
  { to: "/treasury", label: "Treasury" },
  { to: "/mse", label: "MSE guard" },
  { to: "/auditor", label: "Auditor" },
];

function PersonaSwitcher() {
  const { persona, setPersona } = useSession();
  const navigate = useNavigate();
  return (
    <label className="persona">
      <span className="persona-label">Acting as</span>
      <select
        value={persona.userId}
        onChange={(e) => {
          setPersona(e.target.value);
          const next = PERSONAS.find((p) => p.userId === e.target.value);
          if (next) navigate(next.home);
        }}
      >
        {(["BUYER", "SUPPLIER", "BANK", "AUDITOR"] as const).map((org) => (
          <optgroup key={org} label={ORG_LABEL[org]}>
            {PERSONAS.filter((p) => p.org === org).map((p) => (
              <option key={p.userId} value={p.userId}>
                {p.name} ({p.role.replace("_", " ").toLowerCase()})
              </option>
            ))}
          </optgroup>
        ))}
      </select>
    </label>
  );
}

function Toasts() {
  const { toasts, dismiss } = useSession();
  return (
    <div className="toasts" role="status" aria-live="polite">
      {toasts.map((t) => (
        <button key={t.id} className={`toast ${t.kind}`} onClick={() => dismiss(t.id)}>
          {t.text}
        </button>
      ))}
    </div>
  );
}

export function App() {
  return (
    <div className="shell">
      <header className="topbar">
        <NavLink to="/" className="brand">
          <img src="/favicon.svg" alt="" width={28} height={28} />
          <span>Anubandh</span>
          <small>payment deals on Drunix</small>
        </NavLink>
        <PersonaSwitcher />
      </header>
      <nav className="tabs" aria-label="Views">
        {NAV.map((n) => (
          <NavLink key={n.to} to={n.to} end={n.end} className={({ isActive }) => (isActive ? "tab active" : "tab")}>
            {n.label}
          </NavLink>
        ))}
      </nav>
      <div className="layout">
        <main className="main">
          <Routes>
            <Route path="/" element={<HomePage />} />
            <Route path="/buyer" element={<BuyerPage />} />
            <Route path="/warehouse" element={<WarehousePage />} />
            <Route path="/supplier" element={<SupplierPage />} />
            <Route path="/bank" element={<BankPage />} />
            <Route path="/treasury" element={<TreasuryPage />} />
            <Route path="/mse" element={<MsePage />} />
            <Route path="/auditor" element={<AuditorPage />} />
            <Route path="/deals/:dealId" element={<DealPage />} />
            <Route path="*" element={<NotFound />} />
          </Routes>
        </main>
        <LiveFeed />
      </div>
      <Toasts />
    </div>
  );
}
