from __future__ import annotations

import lightgbm as lgb
import numpy as np
import pandas as pd


def train_quantile_models(train_df: pd.DataFrame, target_col: str = "days_to_release") -> dict[float, lgb.Booster]:
    if target_col not in train_df or len(train_df) < 20:
        raise ValueError("Need target column and at least 20 samples")
    X = train_df.drop(columns=[target_col]).select_dtypes(include=["number", "bool"])
    if X.empty:
        raise ValueError("No numeric features")
    y = train_df[target_col].clip(lower=0)
    return {q: lgb.train({"objective": "quantile", "alpha": q, "verbosity": -1, "num_threads": 2, "seed": 42, "learning_rate": .06, "num_leaves": 15, "min_data_in_leaf": 10, "feature_pre_filter": False}, lgb.Dataset(X, label=y, free_raw_data=False), num_boost_round=65) for q in (.1, .5, .9)}


def predict_quantiles(models: dict, X: pd.DataFrame) -> pd.DataFrame:
    vals = np.column_stack([np.maximum(0, models[q].predict(X.reindex(columns=models[q].feature_name(), fill_value=0))) for q in (.1, .5, .9)])
    vals.sort(axis=1)
    return pd.DataFrame(vals, columns=["p10", "p50", "p90"], index=X.index)
