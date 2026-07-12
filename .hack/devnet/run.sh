#!/bin/bash
__dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

if [ -f "${__dir}/custom-kurtosis.devnet.config.yaml" ]; then
  config_file="${__dir}/custom-kurtosis.devnet.config.yaml"
else
  config_file="${__dir}/kurtosis.devnet.config.yaml"
fi

## Run devnet using kurtosis
ENCLAVE_NAME="${ENCLAVE_NAME:-buildoor}"
ETHEREUM_PACKAGE="${ETHEREUM_PACKAGE:-github.com/ethpandaops/ethereum-package}"
if kurtosis enclave inspect "$ENCLAVE_NAME" > /dev/null; then
  echo "Kurtosis enclave '$ENCLAVE_NAME' is already up."
else
  kurtosis run "$ETHEREUM_PACKAGE" \
  --image-download always \
  --enclave "$ENCLAVE_NAME" \
  --args-file "${config_file}"
fi

# Get jwtsecret
kurtosis files inspect "$ENCLAVE_NAME" jwt_file jwtsecret | tail -n +1 > "${__dir}/generated-jwtsecret.txt"

ENCLAVE_UUID=$(kurtosis enclave inspect "$ENCLAVE_NAME" --full-uuids | grep 'UUID:' | awk '{print $2}')

# Discover the canonical buildoor instances launched by the ethereum-package
# (additional_services: [buildoor] + buildoor_params.instances). Service names
# follow buildoor-<cl>-<el>-<participant>[-<n>]; container names are
# <service-name>--<uuid>. Sorted ascending, entry 1 is the first canonical
# instance — the one the participant's CL uses as its builder endpoint.
# BUILDOOR_INDEX (1-based) or BUILDOOR_SERVICE select a different instance.
BUILDOOR_INDEX="${BUILDOOR_INDEX:-1}"
mapfile -t BUILDOOR_CONTAINERS < <(docker ps -a -f "label=kurtosis_enclave_uuid=$ENCLAVE_UUID" --format '{{.Names}}' | grep '^buildoor-' | sort)

if [ ${#BUILDOOR_CONTAINERS[@]} -eq 0 ]; then
  echo "Error: no buildoor services found in enclave '$ENCLAVE_NAME'."
  echo "Make sure 'buildoor' is listed under additional_services in ${config_file}."
  exit 1
fi

if [ -n "$BUILDOOR_SERVICE" ]; then
  BUILDOOR_CONTAINER=$(printf '%s\n' "${BUILDOOR_CONTAINERS[@]}" | grep "^${BUILDOOR_SERVICE}--" | head -n 1)
else
  BUILDOOR_CONTAINER="${BUILDOOR_CONTAINERS[$((BUILDOOR_INDEX - 1))]}"
fi

if [ -z "$BUILDOOR_CONTAINER" ]; then
  echo "Error: buildoor instance not found (index: $BUILDOOR_INDEX, service: ${BUILDOOR_SERVICE:-<unset>})."
  echo "Available instances:"
  printf '  %s\n' "${BUILDOOR_CONTAINERS[@]%%--*}"
  exit 1
fi

BUILDOOR_SERVICE="${BUILDOOR_CONTAINER%%--*}"

# Extract the canonical instance's identity + wiring from its container cmd so
# our local replacement can impersonate it exactly (same builder key, wallet,
# advertised builder-api URL and extra-data tag, same CL/EL pair).
BUILDER_MNEMONIC=""
BUILDER_KEY_INDEX="0"
BUILDER_PRIVKEY=""
WALLET_PRIVKEY=""
BUILDER_API_URL=""
EXTRA_DATA=""
CL_URI=""
EL_RPC_URI=""
ENGINE_URI=""
while IFS= read -r arg; do
  case "$arg" in
    --cl-client=*)         CL_URI="${arg#*=}" ;;
    --el-rpc=*)            EL_RPC_URI="${arg#*=}" ;;
    --el-engine-api=*)     ENGINE_URI="${arg#*=}" ;;
    --wallet-privkey=*)    WALLET_PRIVKEY="${arg#*=}" ;;
    --builder-mnemonic=*)  BUILDER_MNEMONIC="${arg#*=}" ;;
    --builder-key-index=*) BUILDER_KEY_INDEX="${arg#*=}" ;;
    --builder-privkey=*)   BUILDER_PRIVKEY="${arg#*=}" ;;
    --builder-api-url=*)   BUILDER_API_URL="${arg#*=}" ;;
    --extra-data=*)        EXTRA_DATA="${arg#*=}" ;;
  esac
done < <(docker inspect --format '{{range .Config.Cmd}}{{println .}}{{end}}' "$BUILDOOR_CONTAINER")

if [ -z "$BUILDER_MNEMONIC" ] && [ -z "$BUILDER_PRIVKEY" ]; then
  echo "Error: could not extract the builder key from '$BUILDOOR_SERVICE'."
  exit 1
fi

# Stop the canonical instance so the replacement can take over its service name
# (docker frees the DNS alias when the container stops).
kurtosis service stop "$ENCLAVE_NAME" "$BUILDOOR_SERVICE" > /dev/null 2>&1 || true

# Resolve the CL/EL containers the canonical instance was wired to
cl_hostport="${CL_URI#http://}"
CL_SERVICE="${cl_hostport%%:*}"
CL_PORT="${cl_hostport##*:}"
el_hostport="${EL_RPC_URI#http://}"
EL_SERVICE="${el_hostport%%:*}"

BEACON_NODE=$(docker ps -q -f "label=kurtosis_enclave_uuid=$ENCLAVE_UUID" -f "name=${CL_SERVICE}--")
EXECUTION_NODE=$(docker ps -q -f "label=kurtosis_enclave_uuid=$ENCLAVE_UUID" -f "name=${EL_SERVICE}--")

if [ -z "$BEACON_NODE" ] || [ -z "$EXECUTION_NODE" ]; then
  echo "Error: could not resolve CL/EL containers for '$BUILDOOR_SERVICE' (cl: $CL_SERVICE, el: $EL_SERVICE)."
  exit 1
fi

# Host-mapped URLs for running the replacement outside docker
BEACON_URL="127.0.0.1:$(docker inspect --format="{{ (index (index .NetworkSettings.Ports \"${CL_PORT}/tcp\") 0).HostPort }}" "$BEACON_NODE")"
ENGINE_URL="127.0.0.1:$(docker inspect --format='{{ (index (index .NetworkSettings.Ports "8551/tcp") 0).HostPort }}' "$EXECUTION_NODE")"
EXECUTION_URL="127.0.0.1:$(docker inspect --format='{{ (index (index .NetworkSettings.Ports "8545/tcp") 0).HostPort }}' "$EXECUTION_NODE")"

# The kurtosis docker network (kt-<enclave>), resolved from the beacon container
DOCKER_NETWORK=$(docker inspect --format '{{range $k, $v := .NetworkSettings.Networks}}{{$k}}{{end}}' "$BEACON_NODE")

# Validator ranges (optional artifact; used by the WebUI to label proposers)
VALIDATOR_RANGES_FILE=""
rm -rf "${__dir}/generated-validator-ranges"
if kurtosis files download "$ENCLAVE_NAME" validator-ranges "${__dir}/generated-validator-ranges" > /dev/null 2>&1 \
   && [ -f "${__dir}/generated-validator-ranges/validator-ranges.yaml" ]; then
  VALIDATOR_RANGES_FILE="${__dir}/generated-validator-ranges/validator-ranges.yaml"
fi

echo "Replacing buildoor instance '$BUILDOOR_SERVICE' (builder key index: $BUILDER_KEY_INDEX)"
echo "  beacon:    $CL_URI (host: http://$BEACON_URL)"
echo "  execution: $EL_RPC_URI (host: http://$EXECUTION_URL)"
echo "  builder api url: $BUILDER_API_URL"

# Write config
cat <<EOF > "${__dir}/generated-vars.env"
BEACON_API="http://$BEACON_URL"
ENGINE_API="http://$ENGINE_URL"
EXECUTION_API="http://$EXECUTION_URL"
JWT_SECRET="${__dir}/generated-jwtsecret.txt"
DOCKER_NETWORK="$DOCKER_NETWORK"
BUILDOOR_SERVICE="$BUILDOOR_SERVICE"
BUILDER_MNEMONIC="$BUILDER_MNEMONIC"
BUILDER_KEY_INDEX="$BUILDER_KEY_INDEX"
BUILDER_PRIVKEY="$BUILDER_PRIVKEY"
WALLET_PRIVKEY="$WALLET_PRIVKEY"
BUILDER_API_URL="$BUILDER_API_URL"
EXTRA_DATA="$EXTRA_DATA"
VALIDATOR_RANGES_FILE="$VALIDATOR_RANGES_FILE"
EOF

cat <<EOF > "${__dir}/generated-vars-docker.env"
BEACON_API="$CL_URI"
ENGINE_API="$ENGINE_URI"
EXECUTION_API="$EL_RPC_URI"
JWT_SECRET="${__dir}/generated-jwtsecret.txt"
DOCKER_NETWORK="$DOCKER_NETWORK"
BUILDOOR_SERVICE="$BUILDOOR_SERVICE"
BUILDER_MNEMONIC="$BUILDER_MNEMONIC"
BUILDER_KEY_INDEX="$BUILDER_KEY_INDEX"
BUILDER_PRIVKEY="$BUILDER_PRIVKEY"
WALLET_PRIVKEY="$WALLET_PRIVKEY"
BUILDER_API_URL="$BUILDER_API_URL"
EXTRA_DATA="$EXTRA_DATA"
VALIDATOR_RANGES_FILE="$VALIDATOR_RANGES_FILE"
EOF
