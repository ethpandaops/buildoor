// Package payload_bidder holds the shared, transport-independent mechanics for
// post-Gloas builder participation: constructing and signing execution payload
// bids and the corresponding execution payload envelope (reveal). Both the ePBS
// p2p submitter and the HTTP Builder API build on it; each supplies its own
// economics (bid value, execution payment) while the construction, hash-tree-
// root (always via dynamic-ssz so preset-dependent sizes resolve), signing
// domain, and fork handling live here.
//
// Everything is fork-agnostic: bids and envelopes use the go-eth2-client
// spec/all union types and read the active fork from the payload, so adding a
// future fork is confined to the spec/all view tables.
package payload_bidder

import (
	"fmt"

	eth2all "github.com/ethpandaops/go-eth2-client/spec/all"
	"github.com/ethpandaops/go-eth2-client/spec/bellatrix"
	"github.com/ethpandaops/go-eth2-client/spec/capella"
	"github.com/ethpandaops/go-eth2-client/spec/deneb"
	"github.com/ethpandaops/go-eth2-client/spec/gloas"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/ethpandaops/go-eth2-client/spec/version"
	dynssz "github.com/pk910/dynamic-ssz"

	"github.com/ethpandaops/buildoor/pkg/signer"
)

// DomainBeaconBuilder is DOMAIN_BEACON_BUILDER — the signing domain for
// execution payload bids and execution payload envelopes.
var DomainBeaconBuilder = phase0.DomainType{0x0B, 0x00, 0x00, 0x00}

// Signer signs execution payload bids and envelopes with the builder's BLS key.
type Signer struct {
	blsSigner *signer.BLSSigner
}

// NewSigner creates a new payload bidder signer.
func NewSigner(blsSigner *signer.BLSSigner) *Signer {
	return &Signer{blsSigner: blsSigner}
}

// Pubkey returns the builder's BLS public key.
func (s *Signer) Pubkey() phase0.BLSPubKey {
	return s.blsSigner.PublicKey()
}

// SignBid signs an execution payload bid. forkVersion must be the fork version
// the consensus client verifies against (the Gloas fork version).
func (s *Signer) SignBid(
	bid *eth2all.ExecutionPayloadBid,
	forkVersion phase0.Version,
	genesisValidatorsRoot phase0.Root,
) (phase0.BLSSignature, error) {
	if bid.Version == version.DataVersionGloas {
		root, err := hashGloasBid(bid)
		if err != nil {
			return phase0.BLSSignature{}, err
		}

		return s.signRoot(root, forkVersion, genesisValidatorsRoot)
	}

	return s.sign(bid, forkVersion, genesisValidatorsRoot)
}

// SignEnvelope signs an execution payload envelope.
func (s *Signer) SignEnvelope(
	envelope *eth2all.ExecutionPayloadEnvelope,
	forkVersion phase0.Version,
	genesisValidatorsRoot phase0.Root,
) (phase0.BLSSignature, error) {
	if envelope.Version == version.DataVersionGloas {
		root, err := hashGloasEnvelope(envelope)
		if err != nil {
			return phase0.BLSSignature{}, err
		}

		return s.signRoot(root, forkVersion, genesisValidatorsRoot)
	}

	return s.sign(envelope, forkVersion, genesisValidatorsRoot)
}

// sign computes the dynssz hash-tree-root of msg (so preset-dependent list
// limits resolve from the global spec rather than the static mainnet preset)
// and signs it under DomainBeaconBuilder.
func (s *Signer) sign(
	msg any,
	forkVersion phase0.Version,
	genesisValidatorsRoot phase0.Root,
) (phase0.BLSSignature, error) {
	msgRoot, err := dynssz.GetGlobalDynSsz().HashTreeRoot(msg)
	if err != nil {
		return phase0.BLSSignature{}, fmt.Errorf("failed to compute hash tree root: %w", err)
	}

	var root phase0.Root

	copy(root[:], msgRoot[:])
	return s.signRoot(root, forkVersion, genesisValidatorsRoot)
}

func (s *Signer) signRoot(
	root phase0.Root,
	forkVersion phase0.Version,
	genesisValidatorsRoot phase0.Root,
) (phase0.BLSSignature, error) {

	domain := signer.ComputeDomain(DomainBeaconBuilder, forkVersion, genesisValidatorsRoot)

	return s.blsSigner.SignWithDomain(root, domain)
}

// gloasBidSigningView pins the Gloas commitments field to the bounded-list
// schema used by the Glamsterdam consensus specification. go-eth2-client
// v0.1.6 models this field as a progressive list, which produces a different
// signing root even though its JSON and wire fields are otherwise identical.
type gloasBidSigningView struct {
	ParentBlockHash       phase0.Hash32              `ssz-size:"32"`
	ParentBlockRoot       phase0.Root                `ssz-size:"32"`
	BlockHash             phase0.Hash32              `ssz-size:"32"`
	PrevRandao            phase0.Root                `ssz-size:"32"`
	FeeRecipient          bellatrix.ExecutionAddress `ssz-size:"20"`
	GasLimit              uint64
	BuilderIndex          gloas.BuilderIndex
	Slot                  phase0.Slot
	Value                 phase0.Gwei
	ExecutionPayment      phase0.Gwei
	BlobKZGCommitments    []deneb.KZGCommitment `ssz-max:"4096" ssz-size:"?,48"`
	ExecutionRequestsRoot phase0.Root           `ssz-size:"32"`
}

func hashGloasBid(bid *eth2all.ExecutionPayloadBid) (phase0.Root, error) {
	view := &gloasBidSigningView{
		ParentBlockHash:       bid.ParentBlockHash,
		ParentBlockRoot:       bid.ParentBlockRoot,
		BlockHash:             bid.BlockHash,
		PrevRandao:            bid.PrevRandao,
		FeeRecipient:          bid.FeeRecipient,
		GasLimit:              bid.GasLimit,
		BuilderIndex:          bid.BuilderIndex,
		Slot:                  bid.Slot,
		Value:                 bid.Value,
		ExecutionPayment:      bid.ExecutionPayment,
		BlobKZGCommitments:    bid.BlobKZGCommitments,
		ExecutionRequestsRoot: bid.ExecutionRequestsRoot,
	}

	root, err := dynssz.GetGlobalDynSsz().HashTreeRoot(view)
	if err != nil {
		return phase0.Root{}, fmt.Errorf("failed to compute Gloas bid hash tree root: %w", err)
	}

	return phase0.Root(root), nil
}

type gloasPayloadSigningView struct {
	ParentHash      phase0.Hash32              `ssz-size:"32"`
	FeeRecipient    bellatrix.ExecutionAddress `ssz-size:"20"`
	StateRoot       phase0.Root                `ssz-size:"32"`
	ReceiptsRoot    phase0.Root                `ssz-size:"32"`
	LogsBloom       [256]byte                  `ssz-size:"256"`
	PrevRandao      [32]byte                   `ssz-size:"32"`
	BlockNumber     uint64
	GasLimit        uint64
	GasUsed         uint64
	Timestamp       uint64
	ExtraData       []byte `ssz-max:"32"`
	BaseFeePerGas   [32]byte
	BlockHash       phase0.Hash32           `ssz-size:"32"`
	Transactions    []bellatrix.Transaction `ssz-max:"1048576,1073741824" ssz-size:"?,?"`
	Withdrawals     []*capella.Withdrawal   `ssz-max:"16"`
	BlobGasUsed     uint64
	ExcessBlobGas   uint64
	BlockAccessList gloas.BlockAccessList `ssz-max:"1073741824"`
	SlotNumber      uint64
}

type gloasEnvelopeSigningView struct {
	Payload               *gloasPayloadSigningView
	ExecutionRequests     *gloasExecutionRequestsSigningView
	BuilderIndex          gloas.BuilderIndex
	BeaconBlockRoot       phase0.Root `ssz-size:"32"`
	ParentBeaconBlockRoot phase0.Root `ssz-size:"32"`
}

func hashGloasEnvelope(envelope *eth2all.ExecutionPayloadEnvelope) (phase0.Root, error) {
	payload := envelope.Payload
	requests := envelope.ExecutionRequests
	if payload == nil || requests == nil {
		return phase0.Root{}, fmt.Errorf("gloas envelope payload and execution requests are required")
	}

	view := &gloasEnvelopeSigningView{
		Payload: &gloasPayloadSigningView{
			ParentHash:      payload.ParentHash,
			FeeRecipient:    payload.FeeRecipient,
			StateRoot:       payload.StateRoot,
			ReceiptsRoot:    payload.ReceiptsRoot,
			LogsBloom:       payload.LogsBloom,
			PrevRandao:      payload.PrevRandao,
			BlockNumber:     payload.BlockNumber,
			GasLimit:        payload.GasLimit,
			GasUsed:         payload.GasUsed,
			Timestamp:       payload.Timestamp,
			ExtraData:       payload.ExtraData,
			BaseFeePerGas:   payload.BaseFeePerGasLE,
			BlockHash:       payload.BlockHash,
			Transactions:    payload.Transactions,
			Withdrawals:     payload.Withdrawals,
			BlobGasUsed:     payload.BlobGasUsed,
			ExcessBlobGas:   payload.ExcessBlobGas,
			BlockAccessList: payload.BlockAccessList,
			SlotNumber:      payload.SlotNumber,
		},
		ExecutionRequests: &gloasExecutionRequestsSigningView{
			Deposits:        requests.Deposits,
			Withdrawals:     requests.Withdrawals,
			Consolidations:  requests.Consolidations,
			BuilderDeposits: requests.BuilderDeposits,
			BuilderExits:    requests.BuilderExits,
		},
		BuilderIndex:          envelope.BuilderIndex,
		BeaconBlockRoot:       envelope.BeaconBlockRoot,
		ParentBeaconBlockRoot: envelope.ParentBeaconBlockRoot,
	}

	root, err := dynssz.GetGlobalDynSsz().HashTreeRoot(view)
	if err != nil {
		return phase0.Root{}, fmt.Errorf("failed to compute Gloas envelope hash tree root: %w", err)
	}

	return phase0.Root(root), nil
}
