package cmd

import (
	"fmt"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/builder_keys"
	"github.com/ethpandaops/buildoor/pkg/config"
	"github.com/ethpandaops/buildoor/pkg/signer"
)

// newKeyRegistry builds the managed builder key set from the configured entry
// key (raw private key or mnemonic + account index). Internal key 0 is the entry
// key itself, so a single-key deployment keeps its identity.
func newKeyRegistry(cfgSvc *config.Service, log logrus.FieldLogger) (*builder_keys.Registry, error) {
	cfg := cfgSvc.Current()

	entryPrivkey, err := signer.ResolveEntryPrivkey(cfg.BuilderPrivkey, cfg.BuilderMnemonic, cfg.BuilderKeyIndex)
	if err != nil {
		return nil, fmt.Errorf("invalid builder key: %w", err)
	}

	registry, err := builder_keys.NewRegistry(cfgSvc, entryPrivkey, log)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize builder key registry: %w", err)
	}

	return registry, nil
}
