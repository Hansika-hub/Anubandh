from __future__ import annotations

import pandas as pd


def evaluate_alerts(org_id: str, band: pd.DataFrame, probs: pd.Series, threshold: float) -> list[dict]:
    if not 0 <= threshold <= 1:
        raise ValueError("Invalid threshold")
    alerts = []
    for i, prob in enumerate(probs):
        if prob < threshold:
            continue
        ts = pd.Timestamp(band.iloc[i]["ts"])
        alerts.append({"alertId": f"AL-{org_id}-{i:03d}", "orgId": org_id, "severity": "CRITICAL" if prob >= .8 else "WARN", "shortfallProb": round(float(prob), 4), "windowStart": ts.isoformat().replace("+00:00", "Z"), "windowEnd": (ts + pd.Timedelta(days=1)).isoformat().replace("+00:00", "Z"), "drivers": [{"feature": "scheduledOutflows", "impact": 1.0, "text": "Scheduled outflows may exceed available cash buffer"}], "acked": False})
    return alerts
