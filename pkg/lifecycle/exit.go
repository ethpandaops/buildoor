package lifecycle

import (
	"context"
	"fmt"

	"github.com/attestantio/go-eth2-client/spec/phase0"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
	"github.com/ethpandaops/buildoor/pkg/signer"
)

// ExitService handles voluntary exits for builders.
type ExitService struct {
	clClient *beacon.Client
	chainSvc chain.Service
	signer   *signer.BLSSigner
	log      logrus.FieldLogger
}

// NewExitService creates a new exit service.
func NewExitService(
	clClient *beacon.Client,
	chainSvc chain.Service,
	blsSigner *signer.BLSSigner,
	log logrus.FieldLogger,
) *ExitService {
	return &ExitService{
		clClient: clClient,
		chainSvc: chainSvc,
		signer:   blsSigner,
		log:      log.WithField("component", "exit-service"),
	}
}

// CreateVoluntaryExit creates and submits a voluntary exit for the builder.
func (s *ExitService) CreateVoluntaryExit(ctx context.Context, builderIndex uint64) error {
	s.log.WithField("builder_index", builderIndex).Info("Creating voluntary exit")

	// Get current epoch
	currentEpoch, err := s.chainSvc.GetCurrentEpoch(ctx)
	if err != nil {
		return fmt.Errorf("failed to get current epoch: %w", err)
	}

	// Build exit message
	exitMsg := s.buildExitMessage(builderIndex, currentEpoch)

	// Sign exit message
	signedExit, err := s.signExitMessage(exitMsg)
	if err != nil {
		return fmt.Errorf("failed to sign exit: %w", err)
	}

	// Submit exit
	if err := s.clClient.SubmitVoluntaryExit(ctx, signedExit); err != nil {
		return fmt.Errorf("failed to submit exit: %w", err)
	}

	s.log.WithFields(logrus.Fields{
		"builder_index": builderIndex,
		"epoch":         currentEpoch,
	}).Info("Voluntary exit submitted")

	return nil
}

// buildExitMessage creates a voluntary exit message.
func (s *ExitService) buildExitMessage(builderIndex uint64, epoch phase0.Epoch) *phase0.VoluntaryExit {
	return &phase0.VoluntaryExit{
		Epoch:          epoch,
		ValidatorIndex: phase0.ValidatorIndex(builderIndex),
	}
}

// signExitMessage signs a voluntary exit message.
func (s *ExitService) signExitMessage(exit *phase0.VoluntaryExit) (*phase0.SignedVoluntaryExit, error) {
	// Get fork version
	forkVersion, err := s.chainSvc.GetForkVersion(context.Background())
	if err != nil {
		return nil, fmt.Errorf("failed to get fork version: %w", err)
	}

	genesis := s.chainSvc.GetGenesis()

	// Sign the exit
	signature, err := s.signer.SignVoluntaryExit(
		exit.Epoch,
		exit.ValidatorIndex,
		forkVersion,
		genesis.GenesisValidatorsRoot,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to sign exit: %w", err)
	}

	return &phase0.SignedVoluntaryExit{
		Message:   exit,
		Signature: signature,
	}, nil
}

// computeExitDomain computes the domain for exit signing.
func (s *ExitService) computeExitDomain(_ phase0.Epoch) (phase0.Domain, error) {
	// Get fork version
	forkVersion, err := s.chainSvc.GetForkVersion(context.Background())
	if err != nil {
		return phase0.Domain{}, fmt.Errorf("failed to get fork version: %w", err)
	}

	genesis := s.chainSvc.GetGenesis()

	domain := signer.ComputeDomain(
		signer.DomainVoluntaryExit,
		forkVersion,
		genesis.GenesisValidatorsRoot,
	)

	return domain, nil
}
