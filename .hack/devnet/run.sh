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

# Get node RPC URLs
ENCLAVE_UUID=$(kurtosis enclave inspect "$ENCLAVE_NAME" --full-uuids | grep 'UUID:' | awk '{print $2}')

BEACON_NODE=$(docker ps -aq -f "label=kurtosis_enclave_uuid=$ENCLAVE_UUID" \
              -f "label=com.kurtosistech.app-id=kurtosis" \
              -f "label=com.kurtosistech.custom.ethereum-package.client-type=beacon" | tac | head -n 1)

EXECUTION_NODE=$(docker ps -aq -f "label=kurtosis_enclave_uuid=$ENCLAVE_UUID" \
              -f "label=com.kurtosistech.app-id=kurtosis" \
              -f "label=com.kurtosistech.custom.ethereum-package.client-type=execution" | tac | head -n 1)

MEVRELAY_NODES=$(docker ps -aq -f "label=kurtosis_enclave_uuid=$ENCLAVE_UUID" \
              -f "label=com.kurtosistech.app-id=kurtosis" \
              -f "label=com.kurtosistech.id=mev-relay-api" | tac | head -n 1)

# Get URLs from first node of each type
BEACON_URL="127.0.0.1:$(docker inspect --format='{{ (index (index .NetworkSettings.Ports "4000/tcp") 0).HostPort }}' $BEACON_NODE)"
ENGINE_URL="127.0.0.1:$(docker inspect --format='{{ (index (index .NetworkSettings.Ports "8551/tcp") 0).HostPort }}' $EXECUTION_NODE)"
EXECUTION_URL="127.0.0.1:$(docker inspect --format='{{ (index (index .NetworkSettings.Ports "8545/tcp") 0).HostPort }}' $EXECUTION_NODE)"
MEVRELAY_URL="127.0.0.1:$(docker inspect --format='{{ (index (index .NetworkSettings.Ports "9062/tcp") 0).HostPort }}' $MEVRELAY_NODES)"

# Write config
cat <<EOF > "${__dir}/generated-vars.env"
BEACON_API=http://$BEACON_URL
ENGINE_API=http://$ENGINE_URL
EXECUTION_API=http://$EXECUTION_URL
MEVRELAY_API=http://$MEVRELAY_URL
JWT_SECRET=${__dir}/generated-jwtsecret.txt
EOF