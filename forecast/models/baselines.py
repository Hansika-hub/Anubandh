from __future__ import annotations

import numpy as np
import pandas as pd


def moving_average_forecast(history: pd.Series, horizon: int) -> pd.Series:
    if horizon < 1 or history.empty:
        raise ValueError("Nonempty history and positive horizon required")
    return pd.Series(np.repeat(float(history.tail(min(7, len(history))).mean()), horizon), name="forecast")


def prophet_forecast(history: pd.DataFrame, horizon: int) -> pd.DataFrame:
    if not {"ds", "y"}.issubset(history.columns) or horizon < 1:
        raise ValueError("history requires ds,y and positive horizon")
    # Optional dependency; deterministic seasonal-naive fallback for offline demo.
    try:
        from prophet import Prophet
        model = Prophet(daily_seasonality=False)
        model.fit(history[["ds", "y"]])
        return model.predict(model.make_future_dataframe(periods=horizon)).tail(horizon)[["ds", "yhat", "yhat_lower", "yhat_upper"]]
    except ImportError:
        dates = pd.date_range(pd.to_datetime(history.ds).max() + pd.Timedelta(days=1), periods=horizon)
        y = moving_average_forecast(history.y, horizon).to_numpy()
        return pd.DataFrame({"ds": dates, "yhat": y, "yhat_lower": y, "yhat_upper": y})
