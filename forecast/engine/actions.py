from __future__ import annotations

import pandas as pd


def is_mse_safe(action: dict, mse_tranches: pd.DataFrame) -> bool:
    if action.get("actionType") != "RESCHEDULE_NON_MSE":
        return True
    ids = set(action.get("shiftDealIds", []))
    if mse_tranches.empty or not ids:
        return True
    matched = mse_tranches[mse_tranches["dealId"].isin(ids)]
    if matched.empty:
        return True
    if "mseApplies" in matched:
        return not bool(matched["mseApplies"].fillna(False).any())
    return False  # fail closed when MSE status is unknown


def rank_actions(alert: dict, accounts: list[dict], payments: pd.DataFrame) -> list[dict]:
    deficit = max(0, int(alert.get("minBufferPaise", 0)) - int(alert.get("p10Paise", 0)))
    balances = sorted([max(0, int(a.get("balancePaise", 0)) - int(a.get("minBufferPaise", 0))) for a in accounts], reverse=True)
    sweep = min(deficit, sum(balances))
    options = [
        {"actionId": f"{alert['alertId']}-1", "actionType": "SWEEP", "amountPaise": sweep, "estCostPaise": 0, "rationale": "Move available excess cash from other accounts", "mseSafe": True},
        {"actionId": f"{alert['alertId']}-2", "actionType": "DRAW_CREDIT_LINE", "amountPaise": max(0, deficit - sweep), "estCostPaise": max(0, deficit - sweep) * 10 // 10000, "rationale": "Fund residual buffer gap without delaying MSME dues", "mseSafe": True},
    ]
    return [{**a, "rank": i + 1} for i, a in enumerate(options) if a["amountPaise"] > 0]
