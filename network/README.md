# network/ — Fabric/Drunix channel `anubandh-ch` (owner: M3)

Brings up 4 orgs, 5 Fabric CAs with TLS, a Raft orderer and peers, then deploys chaincode `deal`.

| Org | MSP | CA | Peer | Notes |
|-----|-----|----|------|-------|
| Buyer | `BuyerMSP` | :7054 | `peer0.buyer.anubandh.local:7051` | warehouse / approver / treasurer identities |
| Supplier-via-bank | `SupplierMSP` | :8054 | **none** | identities only; endorsed through the Bank peer |
| Bank | `BankMSP` | :9054 | `peer0.bank.anubandh.local:9051` | officer / arbiter / service identities |
| Auditor | `AuditorMSP` | :11054 | `peer0.auditor.anubandh.local:11051` | read-only; **not** in the private collection |
| Orderer | `OrdererMSP` | :10054 | `orderer.anubandh.local:7050` | single Raft node (dev) |

## Usage
```
make network      # CAs -> identities -> orderer/peers -> channel anubandh-ch
make cc-deploy    # package/install/approve/commit chaincode `deal`
make network-down # removes containers, volumes and ALL generated crypto
```
Prerequisites on PATH: `docker`, `go`, and the Fabric 2.5 binaries (`peer`, `configtxgen`, `osnadmin`,
`fabric-ca-client`). Set `FABRIC_CFG_PATH` to the directory that contains `core.yaml`.
Images are overridable (`CA_IMAGE`, `PEER_IMAGE`, `ORDERER_IMAGE`, `COUCH_IMAGE`) so the Drunix images can be dropped in.

## What the chaincode requires (from `chaincode/deal/HANDOFF.md`) and where it is met
| Requirement | Where |
|---|---|
| cert attribute `role` on every identity; `supplierId` on supplier users | `scripts/network.sh` `register` (`--id.attrs role=...:ecert,supplierId=...:ecert`) |
| collection `collBuyerSupplierBank`: BUYER+BANK only, `memberOnlyRead`, `requiredPeerCount 0`, AUDITOR excluded | `config/collections_config.json` |
| endorsement policy allows BUYER+BANK | `scripts/deploy-cc.sh` `--signature-policy "AND('BuyerMSP.peer','BankMSP.peer')"` |
| suppliers run no peers | no supplier peer; `SupplierMSP` is a channel member; gateway points SUPPLIER at the bank peer (`crypto/gateway.env`) |
| identities usable by `gateway/config/users.json` | output layout `crypto/<org>/tlsca.pem`, `crypto/<org>/users/<id>/{cert.pem,key.pem}` |

Lifecycle approval is MAJORITY (3 of 4): Supplier has no peer and cannot approve, so do not change it to ALL.

## Verification status (read this)
- `docker-compose.network.yaml`, `configtx.yaml` and `collections_config.json` are syntax-checked.
- The scripts pass `bash -n` and `shellcheck` (all severities). **They have not been executed end to end** — the authoring environment had no Docker
  daemon and no Fabric binaries.
- **Not verified on a live Drunix network** (same open item as HANDOFF.md): the Drunix image names/versions, its
  SQL state DB (this stack uses CouchDB, the stock-Fabric default; set the Drunix state-DB variables via the
  `CORE_LEDGER_STATE_*` env in the compose file), the `peer lifecycle chaincode` flow, key-range scans and
  `GetHistoryForKey`. Until the Drunix endpoint is confirmed, run the gateway with `FABRIC_MOCK=true`.
- First real run: expect to adjust image tags and may need `sudo`-less access to the Docker socket.
