from __future__ import annotations

import numpy as np


def top_drivers(model, X, k: int = 3) -> list[dict]:
    if X.empty or k < 1:
        return []
    if hasattr(model, "feature_importance"):
        values = model.feature_importance(importance_type="gain")
        names = model.feature_name()
    else:
        values = getattr(model, "feature_importances_", np.zeros(X.shape[1]))
        names = list(X.columns)
    order = np.argsort(np.abs(values))[::-1][:k]
    return [{"feature": names[i], "impact": float(values[i]), "text": f"Model feature importance: {names[i]}"} for i in order]
