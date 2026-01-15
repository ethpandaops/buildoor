// Package contracts provides ABI bindings for smart contracts used by buildoor.
package contracts

import (
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"

	"github.com/ethpandaops/buildoor/pkg/rpc/execution"
)

// BuilderDepositContractABI is the ABI for the builder deposit contract.
// This matches the ePBS builder deposit contract specification.
const BuilderDepositContractABI = `[
	{
		"type": "function",
		"name": "deposit",
		"inputs": [
			{"name": "pubkey", "type": "bytes"},
			{"name": "withdrawal_credentials", "type": "bytes"},
			{"name": "signature", "type": "bytes"},
			{"name": "deposit_data_root", "type": "bytes32"}
		],
		"outputs": [],
		"stateMutability": "payable"
	},
	{
		"type": "event",
		"name": "DepositEvent",
		"inputs": [
			{"name": "pubkey", "type": "bytes", "indexed": false},
			{"name": "withdrawal_credentials", "type": "bytes", "indexed": false},
			{"name": "amount", "type": "bytes", "indexed": false},
			{"name": "signature", "type": "bytes", "indexed": false},
			{"name": "index", "type": "bytes", "indexed": false}
		],
		"anonymous": false
	}
]`

// BuilderDepositContract provides methods for interacting with the builder deposit contract.
type BuilderDepositContract struct {
	address   common.Address
	abi       abi.ABI
	rpcClient *execution.Client
}

// NewBuilderDepositContract creates a new builder deposit contract binding.
func NewBuilderDepositContract(
	address common.Address,
	rpcClient *execution.Client,
) (*BuilderDepositContract, error) {
	parsedABI, err := abi.JSON(strings.NewReader(BuilderDepositContractABI))
	if err != nil {
		return nil, fmt.Errorf("failed to parse contract ABI: %w", err)
	}

	return &BuilderDepositContract{
		address:   address,
		abi:       parsedABI,
		rpcClient: rpcClient,
	}, nil
}

// Address returns the contract address.
func (c *BuilderDepositContract) Address() common.Address {
	return c.address
}

// Deposit creates the transaction data for a builder deposit.
// Parameters:
// - pubkey: 48-byte BLS public key
// - withdrawalCredentials: 32-byte withdrawal credentials (0x03 prefix + padding + address)
// - signature: 96-byte BLS signature
// - depositDataRoot: 32-byte hash tree root of the deposit data
// - amount: deposit amount in Wei
func (c *BuilderDepositContract) Deposit(
	pubkey []byte,
	withdrawalCredentials []byte,
	signature []byte,
	depositDataRoot [32]byte,
	amount *big.Int,
) ([]byte, error) {
	if len(pubkey) != 48 {
		return nil, fmt.Errorf("pubkey must be 48 bytes, got %d", len(pubkey))
	}

	if len(withdrawalCredentials) != 32 {
		return nil, fmt.Errorf("withdrawal credentials must be 32 bytes, got %d", len(withdrawalCredentials))
	}

	if len(signature) != 96 {
		return nil, fmt.Errorf("signature must be 96 bytes, got %d", len(signature))
	}

	data, err := c.abi.Pack("deposit", pubkey, withdrawalCredentials, signature, depositDataRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to pack deposit data: %w", err)
	}

	return data, nil
}

// BuildWithdrawalCredentials builds withdrawal credentials for a builder deposit.
// Format: 0x03 + 00...00 (11 zero bytes) + wallet_address (20 bytes)
func BuildWithdrawalCredentials(walletAddress common.Address) [32]byte {
	var creds [32]byte

	creds[0] = 0x03 // Builder withdrawal prefix

	copy(creds[12:], walletAddress.Bytes())

	return creds
}

// GweiToWei converts Gwei to Wei.
func GweiToWei(gwei uint64) *big.Int {
	wei := new(big.Int).SetUint64(gwei)

	return wei.Mul(wei, big.NewInt(1e9))
}

// WeiToGwei converts Wei to Gwei.
func WeiToGwei(wei *big.Int) uint64 {
	gwei := new(big.Int).Div(wei, big.NewInt(1e9))

	return gwei.Uint64()
}
