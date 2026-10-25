#!/usr/bin/env bash
# Shared helpers for network.sh / deploy-cc.sh. Owner: M3. Sourced, not executed.
# shellcheck disable=SC2034  # variables here are consumed by the scripts that source this file
set -euo pipefail

NET_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ROOT_DIR="$(cd "$NET_DIR/.." && pwd)"
CRYPTO="$NET_DIR/crypto"
ARTIFACTS="$NET_DIR/channel-artifacts"
CHANNEL="${FABRIC_CHANNEL:-anubandh-ch}"
CC_NAME="${FABRIC_CC_NAME:-deal}"
DOMAIN="anubandh.local"
COMPOSE_NET=(docker compose -f "$NET_DIR/docker-compose.network.yaml")

# org:caPort:peerPort  (supplier has a CA but no peer)
ORGS_WITH_PEERS=(buyer:7054:7051 bank:9054:9051 auditor:11054:11051)
ALL_APP_ORGS=(buyer supplier bank auditor)

msp_id() { case "$1" in buyer) echo BuyerMSP;; supplier) echo SupplierMSP;; bank) echo BankMSP;; auditor) echo AuditorMSP;; orderer) echo OrdererMSP;; *) return 1;; esac; }
ca_port() { case "$1" in buyer) echo 7054;; supplier) echo 8054;; bank) echo 9054;; auditor) echo 11054;; orderer) echo 10054;; *) return 1;; esac; }
peer_port() { case "$1" in buyer) echo 7051;; bank) echo 9051;; auditor) echo 11051;; *) return 1;; esac; }

log() { printf '\033[1;34m[network]\033[0m %s\n' "$*" >&2; }
die() { printf '\033[1;31m[network] ERROR:\033[0m %s\n' "$*" >&2; exit 1; }

require_tools() {
  local missing=0
  for t in docker fabric-ca-client configtxgen osnadmin peer; do
    command -v "$t" >/dev/null 2>&1 || { printf 'missing tool: %s\n' "$t" >&2; missing=1; }
  done
  [ "$missing" -eq 0 ] || die "install Hyperledger Fabric 2.5 binaries + fabric-ca-client on PATH (or the Drunix equivalents). See network/README.md."
  export FABRIC_CFG_PATH="${FABRIC_CFG_PATH:-}"
  if [ -z "$FABRIC_CFG_PATH" ] || [ ! -f "$FABRIC_CFG_PATH/core.yaml" ]; then
    die "set FABRIC_CFG_PATH to the directory containing core.yaml (the 'config' dir shipped with the Fabric binaries)."
  fi
}

# Env for the peer CLI acting as <org> admin against that org's peer (reachable on localhost).
peer_env() {
  local org="$1"
  export CORE_PEER_TLS_ENABLED=true
  export CORE_PEER_LOCALMSPID; CORE_PEER_LOCALMSPID="$(msp_id "$org")"
  export CORE_PEER_MSPCONFIGPATH="$CRYPTO/$org/users/admin/msp"
  export CORE_PEER_TLS_ROOTCERT_FILE="$CRYPTO/$org/tlsca.pem"
  local port; port="$(peer_port "$org")"
  export CORE_PEER_ADDRESS="localhost:$port"
  export CORE_PEER_TLS_SERVERHOSTOVERRIDE="peer0.$org.$DOMAIN"
}

ORDERER_ARGS=(-o localhost:7050 --ordererTLSHostnameOverride "orderer.$DOMAIN" --tls --cafile "$CRYPTO/orderer/tlsca.pem")
