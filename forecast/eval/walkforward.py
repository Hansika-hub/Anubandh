from __future__ import annotations

import numpy as np
import pandas as pd

from forecast.models.timing_lgbm import predict_quantiles, train_quantile_models


def walk_forward_eval(df: pd.DataFrame, n_splits: int = 6) -> dict:
    if n_splits < 1 or len(df) < (n_splits + 1) * 20:
        raise ValueError("Insufficient samples")
    df = df.sort_values("createdAt") if "createdAt" in df else df.copy()
    df = df.reset_index(drop=True)
    size = len(df) // (n_splits + 1)
    metrics = []
    for i in range(1, n_splits + 1):
        train = df.iloc[:i * size].drop(columns=["createdAt"], errors="ignore")
        test = df.iloc[i * size: (i + 1) * size].drop(columns=["createdAt"], errors="ignore")
        if test.empty:
            continue
        y = test["days_to_release"].to_numpy()
        pred = predict_quantiles(train_quantile_models(train), test.drop(columns=["days_to_release"]))
        losses = [np.maximum(q * (y - pred[c]), (q - 1) * (y - pred[c])).mean() for q, c in ((.1, "p10"), (.5, "p50"), (.9, "p90"))]
        metrics.append({"pinballLoss": float(np.mean(losses)), "maeP50": float(np.abs(y - pred.p50).mean()), "coverage": float(((y >= pred.p10) & (y <= pred.p90)).mean()), "baselineMae": float(np.abs(y - train.days_to_release.tail(30).mean()).mean())})
    means = {name: float(np.mean([r[name] for r in metrics])) for name in metrics[0]}
    return {**means, "precision": None, "recall": None, "leadDays": None, "folds": metrics, "note": "Alert precision/recall require observed liquidity shortfall labels"}
