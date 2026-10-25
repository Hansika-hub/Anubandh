from __future__ import annotations

import numpy as np
import pandas as pd


def generate_synthetic_year(seed: int = 42, n_suppliers: int = 50, n_customers: int = 30) -> dict[str, pd.DataFrame]:
    if n_suppliers < 1 or n_customers < 1:
        raise ValueError("Counts must be positive")
    rng = np.random.default_rng(seed)
    dates = pd.date_range("2025-01-01", periods=365, tz="UTC")
    profiles = pd.DataFrame({"supplierId": [f"SUP-{i:03d}" for i in range(1, n_suppliers + 1)], "medianDelayDays": rng.integers(2, 18, n_suppliers), "onTimeRate": rng.uniform(.65, .99, n_suppliers), "mseCategory": rng.choice(["MICRO", "SMALL", "MEDIUM", "NONE"], n_suppliers)})
    profiles.loc[profiles.supplierId == "SUP-007", "mseCategory"] = "SMALL"
    deals = []
    receivables = []
    for day in dates:
        for _ in range(3):
            sid = int(rng.integers(n_suppliers))
            delay = max(0, round(profiles.iloc[sid].medianDelayDays + rng.normal(0, 5)))
            amount = int(rng.integers(1_000_000, 20_000_000))
            deals.append({"dealId": f"DL-{day:%Y%m%d}-{len(deals)%10000:04d}", "orgId": "BUYER-001", "supplierId": profiles.iloc[sid].supplierId, "createdAt": day, "dueAt": day + pd.Timedelta(days=delay), "amountPaise": amount, "days_to_release": delay, "status": "FUNDED", "mseCategory": profiles.iloc[sid].mseCategory})
            cid = int(rng.integers(n_customers)) + 1
            receivables.append({"orgId": "BUYER-001", "customerId": f"CUS-{cid:03d}", "dueAt": day + pd.Timedelta(days=int(rng.integers(1, 40))), "amountPaise": int(rng.integers(500_000, 15_000_000)), "probability": float(rng.uniform(.6, 1))})
    cash_accounts = pd.DataFrame([{"orgId": "BUYER-001", "accountId": "CASH-001", "balancePaise": 600_000_000, "minBufferPaise": 200_000_000}, {"orgId": "BUYER-001", "accountId": "CASH-002", "balancePaise": 150_000_000, "minBufferPaise": 30_000_000}])
    recurring = pd.DataFrame([{"orgId": "BUYER-001", "dueAt": day, "amountPaise": 600_000} for day in dates])
    return {"open_deals": pd.DataFrame(deals), "profiles": profiles, "receivables": pd.DataFrame(receivables), "cash_accounts": cash_accounts, "recurring": recurring}


def inject_shock(df: pd.DataFrame, customer_id: str, days_late: int) -> pd.DataFrame:
    if days_late < 0:
        raise ValueError("days_late must be nonnegative")
    result = df.copy(deep=True)
    match = result["customerId"] == customer_id
    result.loc[match, "dueAt"] = pd.to_datetime(result.loc[match, "dueAt"], utc=True) + pd.Timedelta(days=days_late)
    return result
