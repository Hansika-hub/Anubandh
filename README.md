# Anubandh 

**Programmable B2B payment deals with built-in MSME compliance and cash forecasting on Drunix.**

> Money moves only when the goods do, and the treasurer knows before it happens.

Built for the DRUNIX Hackathon.

## Problem

High-value B2B payments run on rails that cannot check conditions like goods received or quality passed. Approvals live outside the payment, partial deliveries are handled by slow manual debit notes, small suppliers are paid late without proof of the acceptance date, and treasurers find cash shortfalls only on the day.

## Solution

Anubandh turns every B2B purchase into a **Smart Payment Deal** on Drunix. Money is released automatically, line by line, when agreed conditions are verified by the right parties.

- **Line-level partial release:** if 80 of 100 units pass inspection, 80% is released at once and the rest is held.
- **MSE payment guard:** tracks the 15/45-day legal payment clock and marks deemed acceptance automatically.
- **Multi-party approvals:** each deal has its own on-chain endorsement policy.
- **Reliable event intake:** gateway, queue and idempotent workers prevent duplicate releases.
- **Liquidity forecast:** LightGBM models give P10/P50/P90 cash bands with ranked actions.

## Tech Stack

Drunix (Go chaincode), Kafka/Redpanda, PostgreSQL, Python (LightGBM, FastAPI), React + TypeScript, Docker Compose.

## Simulated in the Prototype

Bank rails, warehouse goods receipt, GST e-invoice check and Udyam lookup are simulated. Forecast models are trained on synthetic data.

## Getting Started

```bash
git clone https://github.com/<your-team>/anubandh.git
cd anubandh
docker compose up -d
```

Dashboards run at `http://localhost:3000`.

## Tests

- **Burst test:** 500 duplicate and out-of-order events, zero double releases.
- **Partial delivery:** 80 of 100 units accepted, exactly 80% released.
- **Deadline test:** 15 days with no rejection, deemed acceptance.
- **Forecast test:** walk-forward validation against baselines.
