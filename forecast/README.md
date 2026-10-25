# M4 Forecast

Python 3.11. Start at repository root:

```bash
python3.11 -m venv .venv
source .venv/bin/activate
pip install -r forecast/requirements.txt
uvicorn forecast.app.main:app --port 8090
```

Run tests: `python -m pytest tests/forecast -q`.

Routes under `/forecast/v1`: `/health`, `/band`, `/alerts`, `/alerts/{alertId}/actions`, `/alerts/{alertId}/ack`, `/refresh`, `/retrain`, `/whatif`, `/metrics/evaluation`, `/seed/synthetic`.

The service defaults to **synthetic demonstration** and does not assert live ledger integration. `PG_DSN` enables forecast-owned output persistence, but a live read-model adapter remains blocked until M2's actual physical schema is finalized. No non-owned files need modification to run this service locally.
