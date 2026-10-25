#!/usr/bin/env bash
# Package, install, approve and commit the `deal` chaincode on anubandh-ch.   (make cc-deploy)
# Policy (chaincode/deal/HANDOFF.md): endorsement needs BUYER + BANK peers; private collection
# collBuyerSupplierBank has BUYER+BANK members only. Supplier has no peer, so lifecycle approval is
# by Buyer, Bank, Auditor = MAJORITY (3 of 4) as set in configtx.yaml.
# shellcheck source=lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
require_tools

CC_VERSION="${CC_VERSION:-1.0}"
CC_SEQUENCE="${CC_SEQUENCE:-1}"
CC_LABEL="${CC_NAME}_${CC_VERSION}"
CC_POLICY="AND('BuyerMSP.peer','BankMSP.peer')"
COLLECTIONS="$NET_DIR/config/collections_config.json"
BUILD="$NET_DIR/build"
PKG="$BUILD/$CC_LABEL.tar.gz"

[ -f "$CRYPTO/buyer/tlsca.pem" ] || die "network is not up. Run: make network"

# Build from a COPY so chaincode/deal (owned by M1) is never modified.
log "staging chaincode (go mod vendor needs network access to the Go module proxy)"
rm -rf "$BUILD/src" && mkdir -p "$BUILD/src"
cp -R "$ROOT_DIR/chaincode/deal/." "$BUILD/src/"
( cd "$BUILD/src" && go mod vendor )
peer_env buyer
peer lifecycle chaincode package "$PKG" --path "$BUILD/src" --lang golang --label "$CC_LABEL"

for org in buyer bank auditor; do
  peer_env "$org"
  log "install on peer0.$org"
  peer lifecycle chaincode install "$PKG" || log "(already installed? continuing)"
done

peer_env buyer
PKG_ID="$(peer lifecycle chaincode queryinstalled --output json | python3 -c "
import json,sys
label='$CC_LABEL'
for c in json.load(sys.stdin).get('installed_chaincodes',[]):
    if c['label']==label: print(c['package_id']); break")"
[ -n "$PKG_ID" ] || die "package $CC_LABEL not found after install"
log "package id: $PKG_ID"

for org in buyer bank auditor; do
  peer_env "$org"
  log "approve for $org"
  peer lifecycle chaincode approveformyorg "${ORDERER_ARGS[@]}" --channelID "$CHANNEL" --name "$CC_NAME" \
    --version "$CC_VERSION" --package-id "$PKG_ID" --sequence "$CC_SEQUENCE" \
    --signature-policy "$CC_POLICY" --collections-config "$COLLECTIONS"
done

peer_env buyer
peer lifecycle chaincode checkcommitreadiness --channelID "$CHANNEL" --name "$CC_NAME" --version "$CC_VERSION" \
  --sequence "$CC_SEQUENCE" --signature-policy "$CC_POLICY" --collections-config "$COLLECTIONS" --output json

if peer lifecycle chaincode querycommitted --channelID "$CHANNEL" --name "$CC_NAME" >/dev/null 2>&1; then
  log "$CC_NAME already committed on $CHANNEL; leaving it as is (bump CC_SEQUENCE/CC_VERSION to upgrade)"
else
  log "commit"
  peer lifecycle chaincode commit "${ORDERER_ARGS[@]}" --channelID "$CHANNEL" --name "$CC_NAME" \
    --version "$CC_VERSION" --sequence "$CC_SEQUENCE" --signature-policy "$CC_POLICY" --collections-config "$COLLECTIONS" \
    --peerAddresses localhost:7051 --tlsRootCertFiles "$CRYPTO/buyer/tlsca.pem" \
    --peerAddresses localhost:9051 --tlsRootCertFiles "$CRYPTO/bank/tlsca.pem" \
    --peerAddresses localhost:11051 --tlsRootCertFiles "$CRYPTO/auditor/tlsca.pem"
fi
peer lifecycle chaincode querycommitted --channelID "$CHANNEL" --name "$CC_NAME"
log "chaincode $CC_NAME deployed. Run the gateway with FABRIC_MOCK=false and: source network/crypto/gateway.env"
