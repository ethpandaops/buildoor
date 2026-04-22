package fulu

import (
	"encoding/binary"
	"fmt"

	"github.com/ethpandaops/go-eth2-client/spec/bellatrix"
	"github.com/ethpandaops/go-eth2-client/spec/electra"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"

	"github.com/ethpandaops/buildoor/pkg/rpc/engine"
)

const (
	depositRequestType       = 0x00
	withdrawalRequestType    = 0x01
	consolidationRequestType = 0x02

	depositRequestSize       = 192 // 48 + 32 + 8 + 96 + 8
	withdrawalRequestSize    = 76  // 20 + 48 + 8
	consolidationRequestSize = 116 // 20 + 48 + 48
)

// ParseExecutionRequests decodes raw EIP-7685 execution request bytes from the
// Engine API into typed electra.ExecutionRequests.
//
// Each element in raw is: [type_prefix_byte || request_1 || request_2 || ...]
// where all requests of the same type are concatenated after a single prefix byte.
func ParseExecutionRequests(raw engine.ExecutionRequests) (*electra.ExecutionRequests, error) {
	result := &electra.ExecutionRequests{}

	for i, entry := range raw {
		if len(entry) == 0 {
			return nil, fmt.Errorf("execution request %d: empty entry", i)
		}

		reqType := entry[0]
		data := entry[1:]

		// An entry with only the type prefix and no data means zero requests of
		// that type — skip it (matches go-ethereum's CalcRequestsHash behavior).
		if len(data) == 0 {
			continue
		}

		switch reqType {
		case depositRequestType:
			deposits, err := parseDepositRequests(data)
			if err != nil {
				return nil, fmt.Errorf("execution request %d: %w", i, err)
			}
			result.Deposits = deposits

		case withdrawalRequestType:
			withdrawals, err := parseWithdrawalRequests(data)
			if err != nil {
				return nil, fmt.Errorf("execution request %d: %w", i, err)
			}
			result.Withdrawals = withdrawals

		case consolidationRequestType:
			consolidations, err := parseConsolidationRequests(data)
			if err != nil {
				return nil, fmt.Errorf("execution request %d: %w", i, err)
			}
			result.Consolidations = consolidations

		default:
			return nil, fmt.Errorf("execution request %d: unknown type 0x%02x", i, reqType)
		}
	}

	return result, nil
}

func parseDepositRequests(data []byte) ([]*electra.DepositRequest, error) {
	if len(data)%depositRequestSize != 0 {
		return nil, fmt.Errorf("deposit requests: length %d not divisible by %d", len(data), depositRequestSize)
	}

	count := len(data) / depositRequestSize
	deposits := make([]*electra.DepositRequest, count)

	for i := range count {
		d := data[i*depositRequestSize : (i+1)*depositRequestSize]

		var pubkey phase0.BLSPubKey
		copy(pubkey[:], d[0:48])

		withdrawalCreds := make([]byte, 32)
		copy(withdrawalCreds, d[48:80])

		amount := phase0.Gwei(binary.LittleEndian.Uint64(d[80:88]))

		var sig phase0.BLSSignature
		copy(sig[:], d[88:184])

		index := binary.LittleEndian.Uint64(d[184:192])

		deposits[i] = &electra.DepositRequest{
			Pubkey:                pubkey,
			WithdrawalCredentials: withdrawalCreds,
			Amount:                amount,
			Signature:             sig,
			Index:                 index,
		}
	}

	return deposits, nil
}

func parseWithdrawalRequests(data []byte) ([]*electra.WithdrawalRequest, error) {
	if len(data)%withdrawalRequestSize != 0 {
		return nil, fmt.Errorf("withdrawal requests: length %d not divisible by %d", len(data), withdrawalRequestSize)
	}

	count := len(data) / withdrawalRequestSize
	withdrawals := make([]*electra.WithdrawalRequest, count)

	for i := range count {
		d := data[i*withdrawalRequestSize : (i+1)*withdrawalRequestSize]

		var addr bellatrix.ExecutionAddress
		copy(addr[:], d[0:20])

		var pubkey phase0.BLSPubKey
		copy(pubkey[:], d[20:68])

		amount := phase0.Gwei(binary.LittleEndian.Uint64(d[68:76]))

		withdrawals[i] = &electra.WithdrawalRequest{
			SourceAddress:   addr,
			ValidatorPubkey: pubkey,
			Amount:          amount,
		}
	}

	return withdrawals, nil
}

func parseConsolidationRequests(data []byte) ([]*electra.ConsolidationRequest, error) {
	if len(data)%consolidationRequestSize != 0 {
		return nil, fmt.Errorf("consolidation requests: length %d not divisible by %d", len(data), consolidationRequestSize)
	}

	count := len(data) / consolidationRequestSize
	consolidations := make([]*electra.ConsolidationRequest, count)

	for i := range count {
		d := data[i*consolidationRequestSize : (i+1)*consolidationRequestSize]

		var addr bellatrix.ExecutionAddress
		copy(addr[:], d[0:20])

		var sourcePubkey phase0.BLSPubKey
		copy(sourcePubkey[:], d[20:68])

		var targetPubkey phase0.BLSPubKey
		copy(targetPubkey[:], d[68:116])

		consolidations[i] = &electra.ConsolidationRequest{
			SourceAddress: addr,
			SourcePubkey:  sourcePubkey,
			TargetPubkey:  targetPubkey,
		}
	}

	return consolidations, nil
}
