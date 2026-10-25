#!/usr/bin/env bash
# Bring the Anubandh channel network up or down.   Usage: network.sh up|down|status
# Builds: CAs -> identities (with cert attribute `role`) -> orderer + peers -> channel anubandh-ch.
# NOT verified against a live Drunix network (see network/README.md "Verification status").
# shellcheck source=lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

ca_tls() { echo "$CRYPTO/ca/$1/tls-cert.pem"; }

ca_client() { # ca_client <org> <args...>  (FABRIC_CA_CLIENT_HOME is per-org)
  local org="$1"; shift
  FABRIC_CA_CLIENT_HOME="$CRYPTO/$org/ca-client" fabric-ca-client "$@" \
    --caname "ca-$org" --tls.certfiles "$(ca_tls "$org")"
}

write_nodeou() { # write_nodeou <msp dir>
  cat > "$1/config.yaml" <<YAML
NodeOUs:
  Enable: true
  ClientOUIdentifier:  { Certificate: cacerts/ca.pem, OrganizationalUnitIdentifier: client }
  PeerOUIdentifier:    { Certificate: cacerts/ca.pem, OrganizationalUnitIdentifier: peer }
  AdminOUIdentifier:   { Certificate: cacerts/ca.pem, OrganizationalUnitIdentifier: admin }
  OrdererOUIdentifier: { Certificate: cacerts/ca.pem, OrganizationalUnitIdentifier: orderer }
YAML
}

register() { # register <org> <name> <type> [attrs]
  local org="$1" name="$2" type="$3" attrs="${4:-}" out
  local args=(register --id.name "$name" --id.secret "${name}pw" --id.type "$type" -u "https://localhost:$(ca_port "$org")")
  [ -n "$attrs" ] && args+=(--id.attrs "$attrs")
  if ! out=$(ca_client "$org" "${args[@]}" 2>&1); then
    grep -q "already registered" <<<"$out" || { echo "$out" >&2; die "register $name@$org failed"; }
  fi
}

enroll_msp() { # enroll_msp <org> <name> <outdir> [extra enroll args]
  local org="$1" name="$2" out="$3"; shift 3
  ca_client "$org" enroll -u "https://${name}:${name}pw@localhost:$(ca_port "$org")" -M "$out" "$@" >/dev/null
  cp "$CRYPTO/$org/msp/config.yaml" "$out/config.yaml"
  # NodeOU config points at cacerts/ca.pem; fabric-ca names the file by CA URL, so rename it.
  local f
  for f in "$out"/cacerts/*.pem; do
    [ "$(basename "$f")" = ca.pem ] || mv "$f" "$out/cacerts/ca.pem"
    break
  done
}

org_msp_dir() { # org-level MSP (public certs only)
  local org="$1"
  mkdir -p "$CRYPTO/$org/msp/cacerts" "$CRYPTO/$org/msp/tlscacerts"
  cp "$CRYPTO/ca/$org/ca-cert.pem" "$CRYPTO/$org/msp/cacerts/ca.pem"
  cp "$CRYPTO/ca/$org/ca-cert.pem" "$CRYPTO/$org/msp/tlscacerts/ca.pem"
  cp "$CRYPTO/ca/$org/ca-cert.pem" "$CRYPTO/$org/tlsca.pem"
  write_nodeou "$CRYPTO/$org/msp"
}

flatten_keypair() { # flatten_keypair <msp dir> <dest dir> : cert.pem + key.pem for the gateway
  mkdir -p "$2"
  cp "$1"/signcerts/*.pem "$2/cert.pem"
  cp "$1"/keystore/*_sk "$2/key.pem"
  chmod 600 "$2/key.pem"
}

# userId:role[:supplierId]   -- must match gateway/config/users.json (BUILD.md; HANDOFF.md needs attr `role`)
BUYER_USERS=(buyer-warehouse:WAREHOUSE buyer-planthead:PLANT_HEAD buyer-cfo:CFO buyer-procurement:PROCUREMENT buyer-treasurer:TREASURER svc-buyer:PROCUREMENT svc-warehouse:WAREHOUSE)
SUPPLIER_USERS=(supplier-007:SUPPLIER_USER:SUP-007 supplier-012:SUPPLIER_USER:SUP-012)
BANK_USERS=(bank-officer:BANK_OFFICER bank-arbiter:ARBITER svc-bank:BANK_OFFICER)
AUDITOR_USERS=(auditor:AUDITOR)

users_of() { case "$1" in buyer) printf '%s\n' "${BUYER_USERS[@]}";; supplier) printf '%s\n' "${SUPPLIER_USERS[@]}";; bank) printf '%s\n' "${BANK_USERS[@]}";; auditor) printf '%s\n' "${AUDITOR_USERS[@]}";; esac; }

setup_org() {
  local org="$1"
  log "identities for $org"
  mkdir -p "$CRYPTO/$org/ca-client" "$CRYPTO/$org/users"
  org_msp_dir "$org"
  ca_client "$org" enroll -u "https://admin:adminpw@localhost:$(ca_port "$org")" >/dev/null   # bootstrap admin (registrar)

  register "$org" "${org}admin" admin "role=ADMIN:ecert"
  enroll_msp "$org" "${org}admin" "$CRYPTO/$org/users/admin/msp"

  if [ "$org" != supplier ]; then
    register "$org" "peer0$org" peer
    enroll_msp "$org" "peer0$org" "$CRYPTO/$org/peers/peer0/msp"
    enroll_msp "$org" "peer0$org" "$CRYPTO/$org/peers/peer0/tls" --enrollment.profile tls --csr.hosts "peer0.$org.$DOMAIN,localhost"
    local t="$CRYPTO/$org/peers/peer0/tls"
    cp "$t"/signcerts/*.pem "$t/server.crt"; cp "$t"/keystore/*_sk "$t/server.key"; cp "$t"/tlscacerts/*.pem "$t/ca.crt"
  fi

  local spec id role sup attrs
  while IFS= read -r spec; do
    IFS=: read -r id role sup <<<"$spec"
    attrs="role=${role}:ecert"
    [ -n "${sup:-}" ] && attrs="${attrs},supplierId=${sup}:ecert"
    register "$org" "$id" client "$attrs"
    enroll_msp "$org" "$id" "$CRYPTO/$org/users/$id/msp"
    flatten_keypair "$CRYPTO/$org/users/$id/msp" "$CRYPTO/$org/users/$id"
  done < <(users_of "$org")
}

setup_orderer() {
  local org=orderer o="$CRYPTO/orderer/orderers/orderer.$DOMAIN"
  log "identities for orderer"
  mkdir -p "$CRYPTO/$org/ca-client" "$o" "$CRYPTO/$org/users"
  org_msp_dir "$org"
  ca_client "$org" enroll -u "https://admin:adminpw@localhost:$(ca_port "$org")" >/dev/null
  register "$org" orderer orderer
  register "$org" ordereradmin admin
  enroll_msp "$org" orderer "$o/msp"
  enroll_msp "$org" orderer "$o/tls" --enrollment.profile tls --csr.hosts "orderer.$DOMAIN,localhost"
  cp "$o/tls/signcerts/"*.pem "$o/tls/server.crt"; cp "$o/tls/keystore/"*_sk "$o/tls/server.key"; cp "$o/tls/tlscacerts/"*.pem "$o/tls/ca.crt"
  enroll_msp "$org" ordereradmin "$CRYPTO/$org/users/admin/msp"
}

wait_for() { # wait_for <what> <command...>
  local what="$1"; shift
  for _ in $(seq 1 60); do "$@" >/dev/null 2>&1 && return 0; sleep 1; done
  die "timed out waiting for $what"
}

up() {
  require_tools
  mkdir -p "$CRYPTO/ca" "$ARTIFACTS"
  for o in orderer buyer supplier bank auditor; do mkdir -p "$CRYPTO/ca/$o"; done

  log "starting certificate authorities"
  "${COMPOSE_NET[@]}" up -d ca.orderer ca.buyer ca.supplier ca.bank ca.auditor
  for o in orderer buyer supplier bank auditor; do wait_for "CA $o TLS cert" test -s "$CRYPTO/ca/$o/tls-cert.pem"; done

  setup_orderer
  for o in "${ALL_APP_ORGS[@]}"; do setup_org "$o"; done
  # Supplier-via-bank: suppliers have no peer, so the gateway reaches SupplierMSP identities through
  # the bank peer. Give the supplier org the bank peer's TLS CA so users.json paths resolve.
  cp "$CRYPTO/bank/tlsca.pem" "$CRYPTO/supplier/tlsca.pem"

  log "starting orderer and peers"
  "${COMPOSE_NET[@]}" up -d
  wait_for "orderer admin port" bash -c "</dev/tcp/localhost/7053"

  log "creating channel $CHANNEL"
  FABRIC_CFG_PATH="$NET_DIR/config" configtxgen -profile AnubandhChannel -channelID "$CHANNEL" -outputBlock "$ARTIFACTS/$CHANNEL.block"
  local o="$CRYPTO/orderer/orderers/orderer.$DOMAIN/tls"
  osnadmin channel join --channelID "$CHANNEL" --config-block "$ARTIFACTS/$CHANNEL.block" \
    -o localhost:7053 --ca-file "$o/ca.crt" --client-cert "$o/server.crt" --client-key "$o/server.key"

  local spec org
  for spec in "${ORGS_WITH_PEERS[@]}"; do
    org="${spec%%:*}"; peer_env "$org"
    wait_for "peer0.$org" bash -c "</dev/tcp/localhost/$(peer_port "$org")"
    log "joining peer0.$org to $CHANNEL"
    peer channel join -b "$ARTIFACTS/$CHANNEL.block"
  done
  write_gateway_env
  log "network up. Next: make cc-deploy"
}

write_gateway_env() {
  local f="$CRYPTO/gateway.env" c="$CRYPTO/gateway.compose.env"
  {
    echo "# Generated by network.sh. Source before running the gateway on the HOST."
    echo "FABRIC_CRYPTO_DIR=$CRYPTO"
    echo "FABRIC_PEER_BUYER=localhost:7051";   echo "FABRIC_HOST_BUYER=peer0.buyer.$DOMAIN"
    echo "FABRIC_PEER_BANK=localhost:9051";    echo "FABRIC_HOST_BANK=peer0.bank.$DOMAIN"
    echo "FABRIC_PEER_AUDITOR=localhost:11051"; echo "FABRIC_HOST_AUDITOR=peer0.auditor.$DOMAIN"
    echo "# SupplierMSP has no peer: endorsement goes through the bank peer."
    echo "FABRIC_PEER_SUPPLIER=localhost:9051"; echo "FABRIC_HOST_SUPPLIER=peer0.bank.$DOMAIN"
  } > "$f"
  sed -e 's#localhost:7051#peer0.buyer.'"$DOMAIN"':7051#' -e 's#localhost:9051#peer0.bank.'"$DOMAIN"':9051#' \
      -e 's#localhost:11051#peer0.auditor.'"$DOMAIN"':11051#' -e 's#^FABRIC_CRYPTO_DIR=.*#FABRIC_CRYPTO_DIR=/fabric-crypto#' \
      -e 's#^\# Source before.*#\# Use as env_file for the gateway INSIDE the anubandh-net docker network.#' "$f" > "$c"
  log "wrote $f and $c"
}

down() {
  "${COMPOSE_NET[@]}" down -v --remove-orphans
  # Crypto and artifacts are disposable and regenerated by `up`. Keep deletion scoped to them.
  rm -rf "${CRYPTO:?}" "${ARTIFACTS:?}" "$NET_DIR/build"
  log "network removed"
}

case "${1:-}" in
  up) up ;;
  down) down ;;
  status) "${COMPOSE_NET[@]}" ps ;;
  *) echo "usage: $0 up|down|status" >&2; exit 2 ;;
esac
