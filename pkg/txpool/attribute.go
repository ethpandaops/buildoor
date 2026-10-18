package txpool

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/ethereum/go-ethereum/common"
)

// Attribution names the transactions a failed testing_buildBlockV1 call
// points at. Exactly one of Sender, Index or Bisect is set; Reason is the
// normalized failure class used for strikes and metrics.
type Attribution struct {
	Reason string
	Sender *common.Address // every tx of this sender is dropped from the attempt
	Index  int             // a single position of the submitted list is dropped (-1 when unset)
	Bisect bool            // no attribution possible: drop the second half
}

var (
	attributeAddressRe = regexp.MustCompile(`address (0x[0-9a-fA-F]{40})`)
	attributeIndexRe   = regexp.MustCompile(`invalid transaction (\d+)`)
)

// Attribute maps an EL build error text to the offending position of the
// submitted list. geth reports state failures with the sender address
// (nonce, funds, fee cap), decode failures with the tx index, and pool caps
// without either; other ELs mostly follow the same wording. An
// unrecognized error falls back to bisecting the list.
func Attribute(err error, submitted []*PooledTx) Attribution {
	msg := err.Error()

	if m := attributeIndexRe.FindStringSubmatch(msg); m != nil {
		idx, _ := strconv.Atoi(m[1])
		if idx >= 0 && idx < len(submitted) {
			return Attribution{Reason: "invalid_tx", Index: idx}
		}
	}

	if m := attributeAddressRe.FindStringSubmatch(msg); m != nil {
		addr := common.HexToAddress(m[1])

		return Attribution{Reason: classifyBuildError(msg), Sender: &addr, Index: -1}
	}

	switch {
	case strings.Contains(msg, "gas limit reached"):
		return Attribution{Reason: "gas_limit_reached", Index: lastIndex(submitted, func(*PooledTx) bool { return true })}
	case strings.Contains(msg, "max data blobs reached"):
		return Attribution{Reason: "blob_limit_reached", Index: lastIndex(submitted, func(e *PooledTx) bool {
			return len(e.Tx.BlobHashes()) > 0
		})}
	}

	return Attribution{Reason: "unknown", Index: -1, Bisect: true}
}

func classifyBuildError(msg string) string {
	switch {
	case strings.Contains(msg, "nonce too low"):
		return "nonce_too_low"
	case strings.Contains(msg, "nonce too high"):
		return "nonce_too_high"
	case strings.Contains(msg, "insufficient funds"):
		return "insufficient_funds"
	case strings.Contains(msg, "max fee per gas less than block base fee"):
		return "fee_too_low"
	case strings.Contains(msg, "intrinsic gas"):
		return "intrinsic_gas"
	default:
		return "invalid_tx"
	}
}

func lastIndex(submitted []*PooledTx, match func(*PooledTx) bool) int {
	for i := len(submitted) - 1; i >= 0; i-- {
		if match(submitted[i]) {
			return i
		}
	}

	return -1
}

// Apply removes the attributed transactions from the submitted list and
// returns the remaining entries with the dropped ones. Later transactions of
// a dropped sender depend on the dropped nonce and go with it.
func (a Attribution) Apply(submitted []*PooledTx) (kept, dropped []*PooledTx) {
	switch {
	case a.Sender != nil:
		for _, e := range submitted {
			if e.Sender == *a.Sender {
				dropped = append(dropped, e)
			} else {
				kept = append(kept, e)
			}
		}
	case a.Index >= 0 && a.Index < len(submitted):
		sender := submitted[a.Index].Sender
		kept = append(kept, submitted[:a.Index]...)
		dropped = append(dropped, submitted[a.Index])

		for _, e := range submitted[a.Index+1:] {
			if e.Sender == sender {
				dropped = append(dropped, e)
			} else {
				kept = append(kept, e)
			}
		}
	case a.Bisect:
		half := (len(submitted) + 1) / 2
		kept = append(kept, submitted[:half]...)
		dropped = append(dropped, submitted[half:]...)
	default:
		kept = submitted
	}

	return kept, dropped
}
