package legacy

import (
	eth2all "github.com/ethpandaops/go-eth2-client/spec/all"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/ethpandaops/go-eth2-client/spec/version"
	"github.com/holiman/uint256"

	legacytypes "github.com/ethpandaops/buildoor/pkg/builderapi/legacy/types"
	"github.com/ethpandaops/buildoor/pkg/payload_builder"
	"github.com/ethpandaops/buildoor/pkg/signer"
)

// BidSigner signs a BuilderBid and returns the signature.
type BidSigner interface {
	SignWithDomain(root phase0.Root, domain phase0.Domain) (phase0.BLSSignature, error)
}

// BuildSignedBuilderBid builds a SignedBuilderBid for the given fork from a Payload and the
// proposer's pubkey, and signs it with the builder's BLS key using DOMAIN_APPLICATION_BUILDER
// with the provided genesis fork version and a zero genesis validators root (matches
// mev-boost-relay behavior). The bid carries the fork's field set (blob KZG commitments from
// Deneb, execution requests from Electra).
// subsidyGwei is added to the bid value so the proposer sees a higher bid (e.g. for testing).
func BuildSignedBuilderBid(
	event *payload_builder.Payload,
	fork version.DataVersion,
	proposerPubkey phase0.BLSPubKey,
	blsSigner BidSigner,
	subsidyGwei uint64,
	genesisForkVersion phase0.Version,
	maxWithdrawalsPerPayload uint64,
) (*legacytypes.SignedBuilderBid, error) {
	if event == nil || event.ExecutionPayload == nil {
		return nil, nil
	}

	header, err := ExecutionPayloadHeaderFromBeacon(event.ExecutionPayload, fork, maxWithdrawalsPerPayload)
	if err != nil {
		return nil, err
	}

	value, overflow := uint256.FromBig(event.BlockValue)
	if overflow || value == nil {
		value = new(uint256.Int)
	}
	if subsidyGwei > 0 {
		subsidyWei := subsidyGwei * 1_000_000_000
		value.Add(value, new(uint256.Int).SetUint64(subsidyWei))
	}

	bid := &legacytypes.BuilderBid{
		Version: fork,
		Header:  header,
		Value:   value,
		Pubkey:  proposerPubkey,
	}

	if fork >= version.DataVersionDeneb && event.BlobsBundle != nil {
		bid.BlobKZGCommitments = event.BlobsBundle.Commitments
	}

	// Execution requests exist from Electra onwards; builder deposit/exit
	// requests (Gloas+) do not exist in the legacy dialect's spec versions.
	if fork >= version.DataVersionElectra {
		execRequests := &eth2all.ExecutionRequests{Version: fork}
		if event.ExecutionRequests != nil {
			execRequests.Deposits = event.ExecutionRequests.Deposits
			execRequests.Withdrawals = event.ExecutionRequests.Withdrawals
			execRequests.Consolidations = event.ExecutionRequests.Consolidations
		}
		bid.ExecutionRequests = execRequests
	}

	bidRoot, err := bid.HashTreeRoot()
	if err != nil {
		return nil, err
	}

	var root phase0.Root
	copy(root[:], bidRoot[:])
	var zeroRoot phase0.Root

	domain := signer.ComputeDomain(
		signer.DomainApplicationBuilder,
		genesisForkVersion,
		zeroRoot,
	)

	sig, err := blsSigner.SignWithDomain(root, domain)
	if err != nil {
		return nil, err
	}

	return &legacytypes.SignedBuilderBid{
		Version:   fork,
		Message:   bid,
		Signature: sig,
	}, nil
}
