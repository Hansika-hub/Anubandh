# Demo script (about 8 minutes)

**Setup, before the audience arrives:** run `make up`, wait until `docker compose -f deploy/docker-compose.yml ps` shows every service as up, and open http://localhost:3000. The persona switcher (top right) is the only control you need. Keep the **Live ledger** panel visible: every chain event appears there as it is committed.

A fully scripted, terminal-only run of the same story is `make demo`. `make test-partial` checks it automatically.

| # | Persona | Where | Do | Say |
|---|---|---|---|---|
| 1 | Procurement | Buyer → **New deal** | SUP-007, 100 × SKU-STEEL-12 at ₹10,000, 45-day term, written agreement. **Create deal**. | "The PO is now a Smart Payment Deal. Udyam says SUP-007 is a small enterprise, so the MSMED payment guard applies. The line price went into a private data collection, not onto the channel." |
| 2 | Bank Officer | open the deal → **Confirm lien** | keep ₹10,00,000 | "The bank has locked the money. The supplier can ship knowing it will be paid." |
| 3 | Warehouse Desk | Warehouse | GRN 100 units, **2 extra copies, shuffled**. **Send GRN**. | "Three copies hit ingest. The log shows 3 accepted, and the ledger shows one GoodsReceived. Idempotency keys hold at every layer." |
| 4 | Warehouse Desk | Warehouse | QC: 80 accepted. **Send QC**. | "80 pass, 20 fail. ₹8L becomes payable now and ₹2L is held. One transaction, five events: quality, two held lines…" |
| 5 | Plant Head | the deal → **Approve** | | "₹8L needs only the Plant Head. Approval triggers the release over NEFT. Watch: TrancheReleased, then SettlementConfirmed with the bank's UTR." Show **Bank → Payment outbox**. |
| 6 | SUP-007 | the deal → held portion → **Ship replacement** (20) | then, as the Warehouse: GRN tranche 2 (20 units), QC 20/0; as the Plant Head: approve | "The held ₹2L is released only for goods that pass. The PO is paid in full, exactly once." |
| 7 | (terminal) | `make test-deadline`, or the deadline e2e test | it creates the ₹60L MICRO deal and moves the mock clock 15 days | "The buyer stayed silent for 15 days, so the goods are **deemed accepted** by law and the 15-day payment clock starts on chain." Show **MSE guard**: the countdown is running. Then as the Plant Head approve, and as Procurement try to release: refused, ₹60L needs the CFO as well. Approve as the CFO: released. |
| 8 | Treasurer | Treasury | toggle **14 days / Intraday** | "P10 to P90 cash band from LightGBM and Monte Carlo. The ranked actions sweep, draw the credit line, or reschedule non-MSE payments. They never defer an MSE payment." Show the model-quality strip: error versus the moving-average baseline. |
| 9 | Auditor | Auditor | **Download CSV** | "Acceptance date, deadline, paid date and on-time flag for every MSE tranche, straight from the ledger. That is MSME Form-I and the Section 43B(h) check, with line prices hidden from the auditor." |

## If something goes wrong

- **A step shows an error toast.** The message carries the chaincode's error code (for example `ERR_POLICY_NOT_SATISFIED`), which is usually the point: say it out loud.
- **The live ledger is grey.** The SSE stream is reconnecting. It replays what it missed (`Last-Event-ID`), so wait a couple of seconds.
- **Payments stay PENDING.** sim-bank is down: `docker compose -f deploy/docker-compose.yml up -d sim-bank`. The outbox retries on its own.
- **You need a clean slate.** `make down && docker volume rm anubandh_pgdata` and then `make up`. The mock chain lives in memory, so restarting the gateway also resets it.
