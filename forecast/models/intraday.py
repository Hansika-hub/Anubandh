from __future__ import annotations

import pandas as pd


def intraday_schedule(payments: pd.DataFrame, opening_balance_paise: int) -> pd.DataFrame:
    result = payments.sort_values("dueAt").copy()
    result["balancePaise"] = opening_balance_paise + result["signedAmountPaise"].astype("int64").cumsum()
    return result
