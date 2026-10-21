package payload_builder

import (
	"bytes"
	"context"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethpandaops/go-eth-engine-client/spec/paris"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/ethpandaops/go-eth2-client/spec/version"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/action_plan"
	"github.com/ethpandaops/buildoor/pkg/config"
	"github.com/ethpandaops/buildoor/pkg/inclusionlist"
	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
)

// InclusionListRequest is a build's inclusion list instruction (FOCIL,
// Heze), from the slot's frozen plan.
type InclusionListRequest struct {
	// Mode: include the view, or omit it (a censoring payload).
	Mode string
	// Omit leaves these transactions out even in include mode.
	Omit map[common.Hash]struct{}
}

// newInclusionListRequest turns the frozen plan's resolution into a request
// (nil = the defaults: include everything).
func newInclusionListRequest(resolved *action_plan.ResolvedInclusionList) *InclusionListRequest {
	if resolved == nil {
		return nil
	}

	req := &InclusionListRequest{Mode: resolved.Mode}

	if len(resolved.OmitTxs) > 0 {
		req.Omit = make(map[common.Hash]struct{}, len(resolved.OmitTxs))
		for _, hash := range resolved.OmitTxs {
			req.Omit[common.HexToHash(hash)] = struct{}{}
		}
	}

	return req
}

// InclusionListBuild records the inclusion list a payload was built
// against: the builder's view of the slot before it (the gossiped lists
// merged with the beacon node's payload attributes list) and what the build
// carried. A payload built from it satisfies those lists: the engine build
// gets the transactions in its attributes, the testing build places every
// one that can execute.
type InclusionListBuild struct {
	// Slot is the inclusion list slot (the payload's slot - 1).
	Slot          phase0.Slot `json:"slot"`
	DependentRoot phase0.Root `json:"dependent_root"`
	Mode          string      `json:"mode"`
	// Lists are the committee lists in the view (Timely of them on time);
	// Equivocators the members whose lists were ignored.
	Lists        int `json:"lists"`
	Timely       int `json:"timely"`
	Equivocators int `json:"equivocators,omitempty"`
	// Transactions is the size of the view; Omitted those left out on
	// purpose (mode omit, or the plan's omit_txs).
	Transactions int `json:"transactions"`
	Omitted      int `json:"omitted,omitempty"`
	// Bits are the committee bits of the view (hex in JSON).
	Bits hexutil.Bytes `json:"bits,omitempty"`
	// StoreVersion is the inclusion list store's version the view was
	// read at (a testing rebuild follows a newer one).
	StoreVersion uint64 `json:"-"`

	// txs is the list the build carries.
	txs [][]byte
}

// resolveInclusionList reads the inclusion list view for a build of attrs;
// nil before Heze.
func (b *PayloadBuilder) resolveInclusionList(
	ctx context.Context,
	attrs *beacon.PayloadAttributesEvent,
	beaconFork version.DataVersion,
	req *InclusionListRequest,
) *InclusionListBuild {
	if beaconFork < version.DataVersionHeze || attrs.ProposalSlot == 0 {
		return nil
	}

	build := &InclusionListBuild{
		Slot: attrs.ProposalSlot - 1,
		Mode: config.InclusionListInclude,
	}

	view := &inclusionlist.View{}
	if b.ilStore != nil {
		build.StoreVersion = b.ilStore.Version()
		view = b.ilStore.View(ctx, build.Slot, attrs.ParentBlockRoot)
	}

	build.DependentRoot = view.DependentRoot
	build.Lists, build.Timely, build.Equivocators = view.Lists, view.Timely, view.Equivocators
	build.Bits = view.Bits

	// The beacon node's own view travels in the attributes: lists it saw
	// that the event stream did not deliver still count.
	txs := inclusionlist.MergeTransactions(view.Transactions, attrs.InclusionListTransactions)
	build.Transactions = len(txs)

	if req != nil && strings.EqualFold(req.Mode, config.InclusionListOmit) {
		build.Mode = config.InclusionListOmit
		build.Omitted = len(txs)
		build.txs = [][]byte{}

		return build
	}

	build.txs = make([][]byte, 0, len(txs))

	for _, tx := range txs {
		if req != nil && len(req.Omit) > 0 {
			if _, omitted := req.Omit[crypto.Keccak256Hash(tx)]; omitted {
				build.Omitted++

				continue
			}
		}

		build.txs = append(build.txs, tx)
	}

	return build
}

// TransactionHashes are the hashes of the list transactions the build
// carried (nil before Heze).
func (i *InclusionListBuild) TransactionHashes() map[common.Hash]struct{} {
	if i == nil {
		return nil
	}

	hashes := make(map[common.Hash]struct{}, len(i.txs))
	for _, tx := range i.txs {
		hashes[crypto.Keccak256Hash(tx)] = struct{}{}
	}

	return hashes
}

// listTransactions is the list a build carries (nil before Heze).
func (i *InclusionListBuild) listTransactions() [][]byte {
	if i == nil {
		return nil
	}

	return i.txs
}

// inclusionListPollInterval is how often a waiting engine build checks for
// new inclusion lists.
const inclusionListPollInterval = 100 * time.Millisecond

// restartWithInclusionList re-reads the inclusion list view for a waiting
// engine build and, when the list it carries changed, restarts the EL's
// build with the new list (forkchoiceUpdated with the updated attributes).
// A payload missing a list transaction that could execute is one fork
// choice will not extend, so a late list is worth a fresh, shorter build.
// Returns the engine build's own prelude copy and the new payload ID, or nil
// when nothing changed or the restart failed (the old build stands).
func (b *PayloadBuilder) restartWithInclusionList(
	ctx context.Context,
	attrs *beacon.PayloadAttributesEvent,
	prelude *buildPrelude,
) (*buildPrelude, paris.PayloadID) {
	inclusionList := b.resolveInclusionList(ctx, attrs, prelude.beaconFork, prelude.ilReq)
	if inclusionList == nil || sameTransactions(inclusionList.txs, prelude.inclusionList.listTransactions()) {
		return nil, paris.PayloadID{}
	}

	// The testing builds read the shared prelude concurrently: the engine
	// build continues on a copy.
	restarted := *prelude
	payloadAttrs := *prelude.payloadAttrs

	payloadAttrs.InclusionListTransactions = make([]paris.Transaction, len(inclusionList.txs))
	for i, tx := range inclusionList.txs {
		payloadAttrs.InclusionListTransactions[i] = paris.Transaction(tx)
	}

	restarted.payloadAttrs = &payloadAttrs
	restarted.inclusionList = inclusionList

	payloadID, err := b.forkchoiceUpdated(ctx, attrs, &restarted, &payloadAttrs)
	if err != nil {
		b.log.WithError(err).WithField("slot", attrs.ProposalSlot).
			Warn("Cannot restart the engine build with the new inclusion list, keeping the old build")

		return nil, paris.PayloadID{}
	}

	b.log.WithFields(logrus.Fields{
		"slot":     attrs.ProposalSlot,
		"lists":    inclusionList.Lists,
		"list_txs": len(inclusionList.txs),
	}).Info("Inclusion list changed during the engine build, restarted it with the new list")

	return &restarted, payloadID
}

// sameTransactions compares two encoded transaction lists.
func sameTransactions(a, b [][]byte) bool {
	if len(a) != len(b) {
		return false
	}

	for i := range a {
		if !bytes.Equal(a[i], b[i]) {
			return false
		}
	}

	return true
}
