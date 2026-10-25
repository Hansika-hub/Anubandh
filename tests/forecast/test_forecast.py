import pandas as pd
from fastapi.testclient import TestClient

from forecast.app.main import app
from forecast.data.generator import generate_synthetic_year, inject_shock
from forecast.engine.actions import is_mse_safe
from forecast.engine.montecarlo import (
    shortfall_probability,
    simulate_cash_paths,
    to_band,
)
from forecast.features.build import build_release_timing_features
from forecast.models.timing_lgbm import predict_quantiles, train_quantile_models

client = TestClient(app)


def test_seed_and_shock():
    data = generate_synthetic_year()
    assert data["profiles"].shape[0] == 50
    assert data["receivables"].customerId.nunique() == 30
    changed = inject_shock(data["receivables"], "CUS-001", 5)
    assert changed.loc[changed.customerId == "CUS-001", "dueAt"].iloc[0] == data["receivables"].loc[data["receivables"].customerId == "CUS-001", "dueAt"].iloc[0] + pd.Timedelta(days=5)


def test_quantile_model():
    d = generate_synthetic_year()
    df = build_release_timing_features(d["open_deals"], d["profiles"])
    prediction = predict_quantiles(train_quantile_models(df.iloc[:200]), df.drop(columns=["days_to_release"]).iloc[200:220])
    assert list(prediction.columns) == ["p10", "p50", "p90"]
    assert (prediction.p10 <= prediction.p50).all()
    assert (prediction.p50 <= prediction.p90).all()


def test_montecarlo_and_mse():
    empty = pd.DataFrame(columns=["dueAt", "amountPaise"])
    paths = simulate_cash_paths(100_000, empty, empty, empty)
    assert paths.shape == (1000, 14)
    assert (paths == 100_000).all()
    assert to_band(paths).p50.eq(100_000).all()
    assert shortfall_probability(paths, 101_000).eq(1).all()
    mse = pd.DataFrame([{"dealId": "a", "mseApplies": True}])
    assert not is_mse_safe({"actionType": "RESCHEDULE_NON_MSE", "shiftDealIds": ["a"]}, mse)
    assert is_mse_safe({"actionType": "SWEEP"}, mse)


def test_api():
    assert client.get("/forecast/v1/health").status_code == 200
    assert client.post("/forecast/v1/seed/synthetic").status_code == 200
    response = client.get("/forecast/v1/band", params={"orgId": "BUYER-001"})
    assert response.status_code == 200, response.text
    assert len(response.json()["points"]) == 14
    assert all(p["p10"] <= p["p50"] <= p["p90"] for p in response.json()["points"])
    assert client.get("/forecast/v1/alerts", params={"orgId": "BUYER-001"}).status_code == 200
    assert client.post("/forecast/v1/whatif", json={"orgId": "BUYER-001", "shiftDealIds": [], "days": 2}).status_code == 200
    assert client.post("/forecast/v1/retrain").status_code == 200
    assert client.get("/forecast/v1/metrics/evaluation").status_code == 200
