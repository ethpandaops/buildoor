package tx_intake

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
	Index  int             // a single plan index is dropped (-1 when unset)
	Bisect bool            // no attribution possible: drop the second half
}

var (
	addressRe = regexp.MustCompile(`address (0x[0-9a-fA-F]{40})`)
	indexRe   = regexp.MustCompile(`invalid transaction (\d+)`)
)

// Attribute maps geth's build error text to the offending plan position.
// geth reports state failures with the sender address (nonce, funds, fee
// cap), decode failures with the tx index, and pool caps without either.
func Attribute(err error, plan []*Entry) Attribution {
	msg := err.Error()

	if m := indexRe.FindStringSubmatch(msg); m != nil {
		idx, _ := strconv.Atoi(m[1])
		if idx >= 0 && idx < len(plan) {
			return Attribution{Reason: "invalid_tx", Index: idx}
		}
	}

	if m := addressRe.FindStringSubmatch(msg); m != nil {
		addr := common.HexToAddress(m[1])
		return Attribution{Reason: classify(msg), Sender: &addr, Index: -1}
	}

	switch {
	case strings.Contains(msg, "gas limit reached"):
		return Attribution{Reason: "gas_limit_reached", Index: lastIndex(plan, func(e *Entry) bool { return true })}
	case strings.Contains(msg, "max data blobs reached"):
		return Attribution{Reason: "blob_limit_reached", Index: lastIndex(plan, func(e *Entry) bool { return len(e.Tx.BlobHashes()) > 0 })}
	}

	return Attribution{Reason: "unknown", Index: -1, Bisect: true}
}

func classify(msg string) string {
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

func lastIndex(plan []*Entry, match func(*Entry) bool) int {
	for i := len(plan) - 1; i >= 0; i-- {
		if match(plan[i]) {
			return i
		}
	}

	return -1
}

// Apply removes the attributed transactions from the plan and returns the
// remaining entries with the dropped ones.
func (a Attribution) Apply(plan []*Entry) (kept, dropped []*Entry) {
	switch {
	case a.Sender != nil:
		for _, e := range plan {
			if e.Sender == *a.Sender {
				dropped = append(dropped, e)
			} else {
				kept = append(kept, e)
			}
		}
	case a.Index >= 0 && a.Index < len(plan):
		// Later txs of the same sender depend on the dropped nonce.
		sender := plan[a.Index].Sender
		kept = append(kept, plan[:a.Index]...)
		dropped = append(dropped, plan[a.Index])

		for _, e := range plan[a.Index+1:] {
			if e.Sender == sender {
				dropped = append(dropped, e)
			} else {
				kept = append(kept, e)
			}
		}
	case a.Bisect:
		half := (len(plan) + 1) / 2
		kept = append(kept, plan[:half]...)
		dropped = append(dropped, plan[half:]...)
	default:
		kept = plan
	}

	return kept, dropped
}
