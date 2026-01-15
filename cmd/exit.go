package cmd

import (
	"context"
	"fmt"

	"github.com/attestantio/go-eth2-client/spec/phase0"
	"github.com/spf13/cobra"

	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
	"github.com/ethpandaops/buildoor/pkg/signer"
)

var exitCmd = &cobra.Command{
	Use:   "exit",
	Short: "Exit builder from the network",
	Long:  `Sends a voluntary exit for the builder to remove it from the builder set.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()

		// Validate required config
		if cfg.BuilderPrivkey == "" {
			return fmt.Errorf("--builder-privkey is required")
		}

		if cfg.CLClient == "" {
			return fmt.Errorf("--cl-client is required")
		}

		// Initialize CL client
		clClient, err := beacon.NewClient(ctx, cfg.CLClient, logger)
		if err != nil {
			return fmt.Errorf("failed to connect to CL: %w", err)
		}
		defer clClient.Close()

		// Initialize BLS signer
		blsSigner, err := signer.NewBLSSigner(cfg.BuilderPrivkey)
		if err != nil {
			return fmt.Errorf("invalid builder key: %w", err)
		}

		pubkey := blsSigner.PublicKey()

		// Get builder index
		builderIndex, _ := cmd.Flags().GetUint64("builder-index")
		if builderIndex == 0 {
			// Look up builder by pubkey
			builderInfo, err := clClient.GetBuilderByPubkey(ctx, pubkey)
			if err != nil {
				return fmt.Errorf("failed to get builder info: %w", err)
			}

			if builderInfo == nil {
				return fmt.Errorf("builder not registered")
			}

			builderIndex = builderInfo.Index
		}

		// Get chain spec and genesis
		chainSpec, err := clClient.GetChainSpec(ctx)
		if err != nil {
			return fmt.Errorf("failed to get chain spec: %w", err)
		}

		genesis, err := clClient.GetGenesis(ctx)
		if err != nil {
			return fmt.Errorf("failed to get genesis: %w", err)
		}

		// Get current epoch
		currentEpoch, err := clClient.GetCurrentEpoch(ctx)
		if err != nil {
			return fmt.Errorf("failed to get current epoch: %w", err)
		}

		// Get fork version
		forkVersion, err := clClient.GetForkVersion(ctx)
		if err != nil {
			return fmt.Errorf("failed to get fork version: %w", err)
		}

		logger.WithFields(map[string]any{
			"builder_index": builderIndex,
			"pubkey":        fmt.Sprintf("%x", pubkey[:8]),
			"epoch":         currentEpoch,
		}).Info("Creating voluntary exit")

		// Sign voluntary exit
		signature, err := blsSigner.SignVoluntaryExit(
			currentEpoch,
			phase0.ValidatorIndex(builderIndex),
			forkVersion,
			genesis.GenesisValidatorsRoot,
		)
		if err != nil {
			return fmt.Errorf("failed to sign exit: %w", err)
		}

		// Submit exit via CL API
		exit := &phase0.SignedVoluntaryExit{
			Message: &phase0.VoluntaryExit{
				Epoch:          currentEpoch,
				ValidatorIndex: phase0.ValidatorIndex(builderIndex),
			},
			Signature: signature,
		}

		if err := clClient.SubmitVoluntaryExit(ctx, exit); err != nil {
			return fmt.Errorf("failed to submit exit: %w", err)
		}

		logger.WithField("builder_index", builderIndex).Info("Voluntary exit submitted")

		// Note about chain spec and genesis - they're fetched but only used for context
		_ = chainSpec

		return nil
	},
}

func init() {
	rootCmd.AddCommand(exitCmd)

	exitCmd.Flags().Uint64("builder-index", 0, "Builder index (if known, skips lookup)")
}
