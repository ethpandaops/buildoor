package chain

import (
	"fmt"

	enginev "github.com/ethpandaops/go-eth-engine-client/spec/version"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/ethpandaops/go-eth2-client/spec/version"
)

// This file is the single place that maps between consensus-layer (beacon)
// fork data versions and execution-layer (Engine API) data versions. Each
// beacon fork that ships an execution payload corresponds to exactly one
// Engine API fork:
//
//	Bellatrix <-> Paris
//	Capella   <-> Shanghai
//	Deneb     <-> Cancun
//	Electra   <-> Prague
//	Fulu      <-> Osaka
//	Gloas     <-> Amsterdam
//	Heze      <-> Bogota

// EngineVersion maps a beacon (consensus) fork to the Engine API (execution)
// data version whose payload structures it carries. It errors for pre-merge
// forks (phase0/altair), which have no execution payload.
func EngineVersion(v version.DataVersion) (enginev.DataVersion, error) {
	switch v {
	case version.DataVersionBellatrix:
		return enginev.DataVersionParis, nil
	case version.DataVersionCapella:
		return enginev.DataVersionShanghai, nil
	case version.DataVersionDeneb:
		return enginev.DataVersionCancun, nil
	case version.DataVersionElectra:
		return enginev.DataVersionPrague, nil
	case version.DataVersionFulu:
		return enginev.DataVersionOsaka, nil
	case version.DataVersionGloas:
		return enginev.DataVersionAmsterdam, nil
	case version.DataVersionHeze:
		return enginev.DataVersionBogota, nil
	default:
		return enginev.DataVersionUnknown, fmt.Errorf("no engine version for beacon fork %s", v)
	}
}

// BeaconVersion maps an Engine API (execution) data version back to the
// canonical beacon (consensus) fork that introduced it.
func BeaconVersion(v enginev.DataVersion) (version.DataVersion, error) {
	switch v {
	case enginev.DataVersionParis:
		return version.DataVersionBellatrix, nil
	case enginev.DataVersionShanghai:
		return version.DataVersionCapella, nil
	case enginev.DataVersionCancun:
		return version.DataVersionDeneb, nil
	case enginev.DataVersionPrague:
		return version.DataVersionElectra, nil
	case enginev.DataVersionOsaka:
		return version.DataVersionFulu, nil
	case enginev.DataVersionAmsterdam:
		return version.DataVersionGloas, nil
	case enginev.DataVersionBogota:
		return version.DataVersionHeze, nil
	default:
		return version.DataVersionUnknown, fmt.Errorf("no beacon version for engine fork %s", v)
	}
}

// ActiveForkAtEpoch returns the highest beacon fork active at the given epoch.
func (s *service) ActiveForkAtEpoch(epoch phase0.Epoch) version.DataVersion {
	latestFork := version.DataVersionPhase0
	for _, forkSchedule := range s.chainSpec.ForkSchedule {
		if forkSchedule.Epoch <= epoch {
			latestFork = forkSchedule.Fork
		} else {
			break
		}
	}

	return latestFork
}
