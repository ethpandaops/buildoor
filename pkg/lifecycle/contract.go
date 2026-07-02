package lifecycle

// This file provides calldata builders and fee helpers for the EIP-8282 builder
// deposit and exit system contracts (Gloas).
//
// Unlike the legacy validator deposit contract (an ABI-encoded function call),
// the builder deposit/exit predeploys follow the EIP-7002/7251 pattern: the
// request is the raw concatenated calldata, the source is implicit (the deposit
// signature for deposits, msg.sender for exits), and a per-request queue fee must
// be paid as msg.value. The fee is derived from the contract's excess counter
// (storage slot 0) via the same fake-exponential formula as EIP-7002.

import (
	"context"
	"encoding/binary"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
)

// Builder system-contract addresses (EIP-8282 genesis predeploys).
// Hard-coded per ethereum-genesis-generator#297; these are proposal-stage
// canonical addresses and match dora's DefaultSystemContractAddresses.
var (
	// BuilderDepositContractAddress is the EIP-8282 builder deposit predeploy.
	BuilderDepositContractAddress = common.HexToAddress("0x00006AE84ed173D4394de5E28F9ED56b28008282")
	// BuilderExitContractAddress is the EIP-8282 builder exit predeploy.
	BuilderExitContractAddress = common.HexToAddress("0x000014574A74c805590AFF9499fc7A690f008282")
)

const (
	// minRequestFee is MIN_*_REQUEST_FEE: the queue fee floor (1 wei) when the
	// contract's request queue is empty (EIP-7002 shared fee mechanism).
	minRequestFee = 1
	// feeUpdateFraction is *_REQUEST_FEE_UPDATE_FRACTION (17): the fake-exponential
	// denominator controlling how steeply the queue fee grows with excess requests.
	feeUpdateFraction = 17
	// queueFeeHeadroom prices the fee a few queue slots ahead of the currently
	// observed excess, so the request still clears if other requests land in the
	// same block(s) before ours is included. Mirrors dora's "add extra fee".
	queueFeeHeadroom = 3

	// builderWithdrawalPrefix is the withdrawal-credential prefix for builder
	// deposits submitted via the EIP-8282 builder deposit contract.
	builderWithdrawalPrefix = 0x00
	// validatorWithdrawalPrefix is the withdrawal-credential prefix used for the
	// pre-Gloas early-onboarding deposit, which goes through the regular validator
	// deposit contract (execution-address / 0x03-style credentials).
	validatorWithdrawalPrefix = 0x03
)

// excessInhibitor is the EXCESS_INHIBITOR (2^256-1) stored in slot 0 of the
// predeploys before GLOAS_FORK_EPOCH. While it is present the contract is not yet
// accepting requests, so callers must wait rather than submit.
var excessInhibitor = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))

// storageReader reads contract storage; satisfied by *execution.Client.
type storageReader interface {
	GetStorageAt(ctx context.Context, account common.Address, slot common.Hash) ([]byte, error)
}

// BuildBuilderDepositCalldata builds the 184-byte builder deposit request calldata:
// pubkey(48) ++ withdrawal_credentials(32) ++ amount(8, big-endian gwei) ++ signature(96).
// The source address is implicit in the deposit signature.
func BuildBuilderDepositCalldata(
	pubkey []byte,
	withdrawalCredentials []byte,
	amountGwei uint64,
	signature []byte,
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

	data := make([]byte, 0, 184)
	data = append(data, pubkey...)
	data = append(data, withdrawalCredentials...)

	var amountBytes [8]byte
	binary.BigEndian.PutUint64(amountBytes[:], amountGwei)
	data = append(data, amountBytes[:]...)
	data = append(data, signature...)

	return data, nil
}

// BuildBuilderExitCalldata builds the 48-byte builder exit request calldata (the
// builder pubkey). The source address is the transaction sender (msg.sender), which
// must match the builder's registered execution address.
func BuildBuilderExitCalldata(pubkey []byte) ([]byte, error) {
	if len(pubkey) != 48 {
		return nil, fmt.Errorf("pubkey must be 48 bytes, got %d", len(pubkey))
	}

	data := make([]byte, 48)
	copy(data, pubkey)

	return data, nil
}

// ReadQueueFee reads the current per-request queue fee (in wei) for a builder
// system contract. It returns active=false while the contract still holds the
// pre-fork excess inhibitor (i.e. before GLOAS_FORK_EPOCH), signalling the caller
// to wait. The returned fee prices in queueFeeHeadroom extra slots for safety.
func ReadQueueFee(
	ctx context.Context,
	reader storageReader,
	contract common.Address,
) (fee *big.Int, active bool, err error) {
	raw, err := reader.GetStorageAt(ctx, contract, common.Hash{})
	if err != nil {
		return nil, false, fmt.Errorf("failed to read queue excess: %w", err)
	}

	excess := new(big.Int).SetBytes(raw)
	if excess.Cmp(excessInhibitor) == 0 {
		return nil, false, nil
	}

	numerator := new(big.Int).Add(excess, big.NewInt(queueFeeHeadroom))

	return fakeExponential(big.NewInt(minRequestFee), numerator, big.NewInt(feeUpdateFraction)), true, nil
}

// fakeExponential approximates factor * e^(numerator/denominator) using integer
// arithmetic, per the EIP-7002 fee calculation:
//
//	i = 1; output = 0; numerator_accum = factor * denominator
//	while numerator_accum > 0:
//	    output += numerator_accum
//	    numerator_accum = (numerator_accum * numerator) // (denominator * i)
//	    i += 1
//	return output // denominator
func fakeExponential(factor, numerator, denominator *big.Int) *big.Int {
	output := big.NewInt(0)
	accum := new(big.Int).Mul(factor, denominator)

	for i := big.NewInt(1); accum.Sign() > 0; i.Add(i, big.NewInt(1)) {
		output.Add(output, accum)

		accum.Mul(accum, numerator)
		accum.Div(accum, new(big.Int).Mul(denominator, i))
	}

	return output.Div(output, denominator)
}

// buildWithdrawalCredentials builds 32-byte withdrawal credentials in the
// execution-address layout: prefix(1) + 11 zero bytes + wallet_address(20).
func buildWithdrawalCredentials(prefix byte, walletAddress common.Address) [32]byte {
	var creds [32]byte

	creds[0] = prefix

	copy(creds[12:], walletAddress.Bytes())

	return creds
}

// BuilderWithdrawalCredentials builds the withdrawal credentials for an EIP-8282
// builder deposit. Format: 0x00 + 00...00 (11 zero bytes) + wallet_address (20 bytes).
func BuilderWithdrawalCredentials(walletAddress common.Address) [32]byte {
	return buildWithdrawalCredentials(builderWithdrawalPrefix, walletAddress)
}

// ValidatorWithdrawalCredentials builds the withdrawal credentials for the pre-Gloas
// early-onboarding deposit, which is submitted through the regular validator deposit
// contract. Format: 0x03 + 00...00 (11 zero bytes) + wallet_address (20 bytes).
func ValidatorWithdrawalCredentials(walletAddress common.Address) [32]byte {
	return buildWithdrawalCredentials(validatorWithdrawalPrefix, walletAddress)
}

// GweiToWei converts Gwei to Wei.
func GweiToWei(gwei uint64) *big.Int {
	wei := new(big.Int).SetUint64(gwei)

	return wei.Mul(wei, big.NewInt(1e9))
}
