from __future__ import annotations

import pandas as pd


def build_release_timing_features(open_deals: pd.DataFrame, profiles: pd.DataFrame) -> pd.DataFrame:
    df = open_deals.merge(profiles.drop(columns=["mseCategory"], errors="ignore"), on="supplierId", how="left", validate="many_to_one")
    df["createdAt"] = pd.to_datetime(df["createdAt"], utc=True)
    df["weekday"] = df["createdAt"].dt.weekday
    df["month"] = df["createdAt"].dt.month
    df["amountLog"] = df["amountPaise"].astype(float).clip(lower=1).map(__import__("math").log)
    df["mseFlag"] = df["mseCategory"].isin(["MICRO", "SMALL"]).astype(int)
    for col in ("medianDelayDays", "onTimeRate"):
        df[col] = df[col].fillna(df[col].median()).fillna(0)
    return df[["medianDelayDays", "onTimeRate", "weekday", "month", "amountLog", "mseFlag"] + (["days_to_release"] if "days_to_release" in df else [])]


def build_inflow_features(receivables: pd.DataFrame) -> pd.DataFrame:
    result = receivables.copy()
    result["dueAt"] = pd.to_datetime(result["dueAt"], utc=True)
    result["weekday"] = result["dueAt"].dt.weekday
    result["month"] = result["dueAt"].dt.month
    result["expectedPaise"] = (result["amountPaise"] * result.get("probability", 1)).round().astype("int64")
    return result
