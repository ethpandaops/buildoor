// Package chain provides epoch-level state management and builder information caching.
package chain

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/ethpandaops/go-eth2-client/spec/version"
)

// ChainSpec holds chain specification parameters.
type ChainSpec struct {
	SecondsPerSlot time.Duration
	SlotsPerEpoch  uint64

	// Duty calculation parameters
	ShuffleRoundCount          uint64
	TargetCommitteeSize        uint64
	MaxCommitteesPerSlot       uint64
	MaxEffectiveBalance        uint64
	MaxEffectiveBalanceElectra uint64
	EpochsPerHistoricalVector  uint64
	MinSeedLookahead           uint64

	// Domain types
	DomainBeaconProposer      phase0.DomainType
	DomainBeaconAttester      phase0.DomainType
	DomainPtcAttester         phase0.DomainType
	DomainProposerPreferences phase0.DomainType

	// Fork epochs and versions (nil if not configured)
	ForkSchedule []ForkSchedule

	// Blob schedule (BPO - Blob Parameters Only)
	BlobSchedule []BlobScheduleEntry

	// ePBS parameters
	PtcSize uint64

	// Deposit contract
	DepositContractAddress *common.Address
}

// BlobScheduleEntry represents a single entry in the BLOB_SCHEDULE.
type BlobScheduleEntry struct {
	Epoch            uint64
	MaxBlobsPerBlock uint64
}

type ForkSchedule struct {
	Fork    version.DataVersion
	Version phase0.Version
	Epoch   phase0.Epoch
}

// ParseChainSpec parses the chain specification from the spec data.
func ParseChainSpec(specData map[string]string, rawData map[string]json.RawMessage) (*ChainSpec, error) {
	spec := &ChainSpec{}
	if err := spec.parseSpecData(specData, rawData); err != nil {
		return nil, fmt.Errorf("failed to parse spec data: %w", err)
	}
	return spec, nil
}

// GetChainSpec fetches the chain specification from the beacon node via direct HTTP.
// This bypasses go-eth2-client's active check so it works before the node is fully ready.
func (s *ChainSpec) parseSpecData(specData map[string]string, rawData map[string]json.RawMessage) error {
	secondsPerSlotStr, ok := specData["SECONDS_PER_SLOT"]
	if !ok {
		return fmt.Errorf("SECONDS_PER_SLOT not found")
	}

	secondsPerSlot, err := strconv.ParseUint(secondsPerSlotStr, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid SECONDS_PER_SLOT: %w", err)
	}

	s.SecondsPerSlot = time.Duration(secondsPerSlot) * time.Second

	slotsPerEpochStr, ok := specData["SLOTS_PER_EPOCH"]
	if !ok {
		return fmt.Errorf("SLOTS_PER_EPOCH not found")
	}

	slotsPerEpoch, err := strconv.ParseUint(slotsPerEpochStr, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid SLOTS_PER_EPOCH: %w", err)
	}

	s.SlotsPerEpoch = slotsPerEpoch

	// Parse duty calculation parameters (use defaults if not present)
	if v, err := parseSpecUint64(specData, "SHUFFLE_ROUND_COUNT"); err == nil {
		s.ShuffleRoundCount = v
	}

	if v, err := parseSpecUint64(specData, "TARGET_COMMITTEE_SIZE"); err == nil {
		s.TargetCommitteeSize = v
	}

	if v, err := parseSpecUint64(specData, "MAX_COMMITTEES_PER_SLOT"); err == nil {
		s.MaxCommitteesPerSlot = v
	}

	if v, err := parseSpecUint64(specData, "MAX_EFFECTIVE_BALANCE"); err == nil {
		s.MaxEffectiveBalance = v
	}

	if v, err := parseSpecUint64(specData, "MAX_EFFECTIVE_BALANCE_ELECTRA"); err == nil {
		s.MaxEffectiveBalanceElectra = v
	}

	if v, err := parseSpecUint64(specData, "EPOCHS_PER_HISTORICAL_VECTOR"); err == nil {
		s.EpochsPerHistoricalVector = v
	}

	if v, err := parseSpecUint64(specData, "MIN_SEED_LOOKAHEAD"); err == nil {
		s.MinSeedLookahead = v
	}

	// Parse domain types
	if v, err := parseSpecDomainType(specData, "DOMAIN_BEACON_PROPOSER"); err == nil {
		s.DomainBeaconProposer = v
	}

	if v, err := parseSpecDomainType(specData, "DOMAIN_BEACON_ATTESTER"); err == nil {
		s.DomainBeaconAttester = v
	}

	if v, err := parseSpecDomainType(specData, "DOMAIN_PTC_ATTESTER"); err == nil {
		s.DomainPtcAttester = v
	}

	if v, err := parseSpecDomainType(specData, "DOMAIN_PROPOSER_PREFERENCES"); err == nil {
		s.DomainProposerPreferences = v
	}

	// Parse fork schedule
	forkSchedule := []ForkSchedule{}
	for key, val := range specData {
		if strings.HasSuffix(key, "_FORK_VERSION") && key != "GENESIS_FORK_VERSION" {
			forkKey := key[:len(key)-len("_FORK_VERSION")]
			fork, err := version.DataVersionFromString(strings.ToLower(forkKey))
			if err != nil {
				return fmt.Errorf("failed to parse fork version: %w", err)
			}

			forkVersionBytes, err := hex.DecodeString(strings.TrimPrefix(val, "0x"))
			if err != nil {
				return fmt.Errorf("failed to decode fork version: %w", err)
			}

			if len(forkVersionBytes) != 4 {
				return fmt.Errorf("invalid fork version length: %d", len(forkVersionBytes))
			}

			var forkVersion phase0.Version
			copy(forkVersion[:], forkVersionBytes)

			var forkEpoch phase0.Epoch
			if forkEpochStr, hasForkEpoch := specData[forkKey+"_FORK_EPOCH"]; hasForkEpoch {
				forkEpochUint, err := strconv.ParseUint(forkEpochStr, 10, 64)
				if err != nil {
					return fmt.Errorf("failed to parse fork epoch: %w", err)
				}

				forkEpoch = phase0.Epoch(forkEpochUint)
			} else {
				forkEpoch = phase0.Epoch(math.MaxUint64)
			}

			forkSchedule = append(forkSchedule, ForkSchedule{
				Fork:    fork,
				Version: forkVersion,
				Epoch:   forkEpoch,
			})
		}
	}

	sort.Slice(forkSchedule, func(i, j int) bool {
		return forkSchedule[i].Fork < forkSchedule[j].Fork
	})

	s.ForkSchedule = forkSchedule

	// Parse ePBS parameters
	if v, err := parseSpecUint64(specData, "PTC_SIZE"); err == nil {
		s.PtcSize = v
	}

	// Parse deposit contract address
	if addrStr, ok := specData["DEPOSIT_CONTRACT_ADDRESS"]; ok {
		addr := common.HexToAddress(addrStr)
		s.DepositContractAddress = &addr
	}

	// Parse blob schedule (BPO)
	if raw, ok := rawData["BLOB_SCHEDULE"]; ok {
		var entries []struct {
			Epoch            string `json:"EPOCH"`
			MaxBlobsPerBlock string `json:"MAX_BLOBS_PER_BLOCK"`
		}
		s.BlobSchedule = []BlobScheduleEntry{}
		if err := json.Unmarshal(raw, &entries); err == nil {
			for _, e := range entries {
				epoch, _ := strconv.ParseUint(e.Epoch, 10, 64)
				maxBlobs, _ := strconv.ParseUint(e.MaxBlobsPerBlock, 10, 64)
				s.BlobSchedule = append(s.BlobSchedule, BlobScheduleEntry{
					Epoch:            epoch,
					MaxBlobsPerBlock: maxBlobs,
				})
			}
		}
	}

	return nil
}

// parseSpecUint64 parses a uint64 value from the spec data map.
func parseSpecUint64(data map[string]string, key string) (uint64, error) {
	s, ok := data[key]
	if !ok {
		return 0, fmt.Errorf("%s not found", key)
	}

	return strconv.ParseUint(s, 10, 64)
}

// parseSpecDomainType parses a 4-byte domain type from the spec data map (hex with 0x prefix).
func parseSpecDomainType(data map[string]string, key string) (phase0.DomainType, error) {
	s, ok := data[key]
	if !ok {
		return phase0.DomainType{}, fmt.Errorf("%s not found", key)
	}

	b, err := hex.DecodeString(strings.TrimPrefix(s, "0x"))
	if err != nil {
		return phase0.DomainType{}, err
	}

	if len(b) != 4 {
		return phase0.DomainType{}, fmt.Errorf("invalid domain type length: %d", len(b))
	}

	var dt phase0.DomainType
	copy(dt[:], b)

	return dt, nil
}

// IsForkScheduled checks if a fork is scheduled.
func (s *ChainSpec) IsForkScheduled(fork version.DataVersion) bool {
	for _, forkSchedule := range s.ForkSchedule {
		if forkSchedule.Fork == fork {
			return forkSchedule.Epoch < math.MaxUint64
		}
	}

	return false
}

// IsForkActive checks if a fork is active for a given epoch.
func (s *ChainSpec) IsForkActive(fork version.DataVersion, epoch phase0.Epoch) bool {
	for _, forkSchedule := range s.ForkSchedule {
		if forkSchedule.Fork == fork {
			return forkSchedule.Epoch <= epoch
		}
	}

	return false
}

// GetForkEpoch returns the epoch of a given fork.
func (s *ChainSpec) GetForkEpoch(fork version.DataVersion) phase0.Epoch {
	for _, forkSchedule := range s.ForkSchedule {
		if forkSchedule.Fork == fork {
			return forkSchedule.Epoch
		}
	}

	return math.MaxUint64
}

// GetForkVersion returns the fork version for a given fork.
func (s *ChainSpec) GetForkVersion(fork version.DataVersion) (phase0.Version, error) {
	for _, forkSchedule := range s.ForkSchedule {
		if forkSchedule.Fork == fork {
			return forkSchedule.Version, nil
		}
	}

	return phase0.Version{}, fmt.Errorf("fork version not found")
}
