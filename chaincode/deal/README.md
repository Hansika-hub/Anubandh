# chaincode/deal  (M1, branch `feat/chaincode`)
Anubandh deal contract — Go, Fabric `fabric-contract-api-go` v1. Drunix is a Hyperledger Fabric v2.5.x-compatible
fork, so this is standard Fabric chaincode. Name `deal`, channel `anubandh-ch`, contract `DealContract`.

    core/        all business logic, zero Fabric imports  (go test runs without a network)
    contract.go  thin Fabric shell: the 29 frozen functions of BUILD §5.3
    fabric_ledger.go  adapts the Fabric stub / client identity to core.Ledger

    cd chaincode/deal
    go test ./core -race -count=1 -cover     # 31 tests, includes the 11 names frozen in BUILD §5.5
    go mod tidy && go build ./...            # needs internet; verifies the Fabric adapter
    go vet ./... && gofmt -l .               # must print nothing

Cert attributes the CA must issue (M3): `role` (always), `supplierId` (supplier users), `userId`, `buyerId` (optional).
Transient map for CreateDeal: key `lines` = JSON array of Line. Private collection: `collBuyerSupplierBank`.
Read HANDOFF.md before integrating; CONTRACT_CHANGE_001.md is the PR text for the 5 owners.
