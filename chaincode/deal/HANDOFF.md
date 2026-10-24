# HANDOFF — what each teammate needs from M1's chaincode

## Calling it (M2 gateway)
`SubmitWithRetry(ctx, fn, args...)`: every arg is a string (int64 as decimal text). Errors come back as `ERR_XXX: text`;
map by prefix. `ERR_INVALID_TRANSITION` = ordering problem (requeue), `ERR_DUPLICATE` = already applied (mark processed).
Fabric's own MVCC failure is separate (`ERR_MVCC_CONFLICT`, retry).
```
CreateDeal       args: [dealJSON]   transient: {"lines": "[{\"lineNo\":1,\"sku\":\"SKU-STEEL-12\",\"orderedQty\":100,\"unitPricePaise\":1000000}]"}
                 dealJSON = {dealId,poNumber,buyerId,supplierId,supplierAccountRef,lienAmountPaise(=sum of lines),toleranceBps,
                   deliveryDeadline,mse:{category,termDays,udyamNo,applies,writtenAgreement},approvalPolicy:[{minPaise,requiredRoles}]}
PostGoodsReceipt [dealId, "1", "[{\"lineNo\":1,\"deliveredQty\":100}]", "warehouse:GR-88123"]
RecordQualityCheck [dealId, "1", "[{\"lineNo\":1,\"acceptedQty\":80,\"rejectedQty\":20}]", "erp:QC-5521"]
ReleaseTranche   [dealId, "1", "bank:REL-<dealId>-T1"]       // idempotency key, one per tranche
```
Identity per call (M2 holds one Fabric identity per role): warehouse GRN = BUYER/WAREHOUSE, QC/approve/release = BUYER,
lien/settlement = BANK, ack/replacement = SUPPLIER_USER, TickDeadlines = BANK.

## Network (M3) — things the chaincode REQUIRES
1. Cert attribute `role` on every identity (`fabric-ca-client register --id.attrs 'role=CFO:ecert'`), plus `supplierId`
   on supplier users. MSP ids assumed `BuyerMSP, BankMSP, SupplierMSP, AuditorMSP` — edit `core/identity.go` if yours differ.
2. Suppliers run no peers (proposal): their identities may be issued by the bank CA; the chaincode accepts a
   `SUPPLIER_USER` under `BankMSP`. Private collection `collBuyerSupplierBank` therefore needs BUYER and BANK peers as members
   (`memberOnlyRead: true`, `requiredPeerCount: 0`). AUDITOR must NOT be a member.
3. Chaincode endorsement policy must allow BUYER+BANK. `CreateDeal` also sets a per-deal key-level policy (BUYER and BANK peers).
4. Confirm on Drunix: `peer lifecycle chaincode` flow and the SQL state DB behave like Fabric 2.5 for key-range scans and
   `GetHistoryForKey`. I verified Drunix's own docs say it is v2.5.x-compatible; I did NOT verify these two calls on a
   running Drunix network (nobody has yet). First task for M1+M3 once `make network` works.

## What was and was not verified (be exact with judges)
Verified here: 31 tests incl. all 11 frozen names, race detector, 78% coverage, 4 deliberately injected bugs all caught,
test ledger models Fabric's no-read-your-writes rule, adapter type-checks against signatures copied from the real
fabric-chaincode-go source, 29/29 frozen function names present, burst test: 500 duplicate+shuffled events, 0 double releases (contract level).
NOT verified: compiling against the real Fabric modules (sandbox could not fetch them), running on Drunix, real endorsement.
Run `go mod tidy && go build ./... && go test ./...` on your laptop first; if `go build` complains, tell me the exact error.

## Honest limits to state before a judge finds them
- **Clock trust.** Fabric's tx timestamp is client-proposed. "Nobody can backdate" is true for signed ledger entries, not for
  wall-clock truth. Mitigation built in: only BANK/BUYER may drive time rules. Say: "clock authority = bank-run scheduler;
  production adds NTP-attested time." Do not claim cryptographic time.
- **GRN date is buyer-posted.** The 15-day window starts at the goods-receipt post. The supplier's `AcknowledgeDelivery`
  is co-signed evidence but a buyer who never posts the GRN starts no clock. Roadmap: supplier-initiated delivery claim.
- **Key-level endorsement is org-level (peers).** CFO/PLANT_HEAD rules are enforced in chaincode from the client cert's
  `role`, not by the endorsement policy. Never describe the CFO check as "endorsement policy".
- Deemed acceptance applies to MSE deals only (non-MSE skip it, per the "scope" limitation in the proposal).

## Git: exactly what to type (M1)
    git clone <repo> && cd anubandh
    git switch -c feat/chaincode
    mkdir -p chaincode/deal && cp -r <unzipped>/deal/* chaincode/deal/        # your folder only
    cd chaincode/deal && go mod tidy && go test ./core -race -count=1 && gofmt -l . && go vet ./...
    cd ../.. && git add chaincode/deal && git commit -m "feat(chaincode): deal contract with partial release, MSE clock, exits"
    git push -u origin feat/chaincode        # open PR -> main, label nothing
    # separate PR for the contract amendment (merge it FIRST):
    git switch main && git pull && git switch -c contract/chaincode-v1
    # put CONTRACT_CHANGE_001.md text into the PR description, apply its edits to contracts/events.md + types.json
    git push -u origin contract/chaincode-v1  # label: contract-change
Daily at 18:00: `git fetch && git rebase origin/main`, run tests, push, PR. Never touch another member's folder.
