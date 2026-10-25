from __future__ import annotations

import numpy as np
import pandas as pd


def simulate_cash_paths(opening_balance_paise: int, obligations: pd.DataFrame, inflows: pd.DataFrame, recurring: pd.DataFrame, n_paths: int = 1000, horizon: str = "14d") -> np.ndarray:
    if n_paths < 1 or opening_balance_paise < 0 or horizon not in ("14d", "intraday"):
        raise ValueError("Invalid simulation arguments")
    steps = 24 if horizon == "intraday" else 14
    start = pd.Timestamp.now(tz="UTC").floor("h" if horizon == "intraday" else "D")
    freq = "h" if horizon == "intraday" else "D"
    ticks = pd.date_range(start, periods=steps, freq=freq)
    rng = np.random.default_rng(42)
    delta = np.zeros((n_paths, steps), dtype=np.int64)
    for df, sign in ((obligations, -1), (inflows, 1), (recurring, -1)):
        if df.empty:
            continue
        for row in df.to_dict("records"):
            due = pd.Timestamp(row.get("dueAt", start))
            due = due.tz_localize("UTC") if due.tzinfo is None else due.tz_convert("UTC")
            idx = int(ticks.searchsorted(due, side="left"))
            # Events beyond horizon are deliberately not included.
            if idx >= steps:
                continue
            amount = int(row.get("amountPaise", 0))
            if amount < 0:
                raise ValueError("Negative amount")
            probability = float(row.get("probability", 1))
            if not 0 <= probability <= 1:
                raise ValueError("Invalid probability")
            offsets = rng.integers(0, 3 if horizon == "14d" else 2, n_paths) if sign == 1 else np.zeros(n_paths, dtype=int)
            indexes = idx + offsets
            valid = indexes < steps
            occurs = rng.random(n_paths) < probability
            rows = np.flatnonzero(valid & occurs)
            delta[rows, indexes[rows]] += sign * amount
    return opening_balance_paise + np.cumsum(delta, axis=1)


def to_band(paths: np.ndarray) -> pd.DataFrame:
    if paths.ndim != 2 or paths.size == 0:
        raise ValueError("Expected nonempty 2D paths")
    q = np.quantile(paths, [.1, .5, .9], axis=0, method="nearest").T.astype("int64")
    return pd.DataFrame({"ts": pd.date_range(pd.Timestamp.now(tz="UTC").floor("D"), periods=paths.shape[1], freq="D"), "p10": q[:, 0], "p50": q[:, 1], "p90": q[:, 2]})


def shortfall_probability(paths: np.ndarray, min_buffer_paise: int) -> pd.Series:
    return pd.Series((paths < min_buffer_paise).mean(axis=0), name="shortfallProb")
