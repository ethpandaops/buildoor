package config

import (
	"fmt"
	"slices"
	"strings"
)

// CandidateKeys are the build-parent candidates a slot can build a payload for.
var CandidateKeys = []string{"parent_full", "parent_empty", "grandparent_full", "grandparent_empty"}

// KeyStrategies are the builder key selection strategies.
var KeyStrategies = []string{"round_robin", "single", "random", "least_used"}

// ValidateSetting checks a typed settings value against what its consumer
// accepts. It is the single validation both sources go through — CLI values
// at startup and UI overrides at write time — because the modules fall back to
// a default for a value they do not recognise: an unvalidated typo would be
// stored, displayed, and then ignored.
func ValidateSetting(key string, v any) error {
	switch key {
	case KeyScheduleMode:
		mode, _ := v.(ScheduleMode)

		switch mode {
		case ScheduleModeAll, ScheduleModeEveryN, ScheduleModeNextN:
		default:
			return fmt.Errorf("%s must be all, every_nth or next_n, got %q", key, mode)
		}

	case KeySlotResultRetentionEpochs, KeySlotArtifactRetentionEpochs:
		return requirePositive(key, v)

	case KeyRevealMaxAttempts:
		return requirePositive(key, v)

	case KeyDepositAmount:
		return requirePositive(key, v)

	case KeyRevealGateMode:
		return requireOneOf(key, v,
			RevealGateTime, RevealGateVote, RevealGateVoteOrTime, RevealGateVoteAndTime)

	case KeyRevealBroadcastValidation:
		return requireOneOf(key, v, BroadcastValidationGossip, BroadcastValidationConsensus,
			BroadcastValidationConsensusAndEquivocation)

	case KeyBuildCandidateParentFull, KeyBuildCandidateParentEmpty,
		KeyBuildCandidateGrandparentFull, KeyBuildCandidateGrandparentEmpty:
		return requireOneOf(key, v, CandidateModeAuto, CandidateModeAlways, CandidateModeNever)

	case KeyEPBSKeyStrategy:
		return requireOneOf(key, v, KeyStrategies...)

	case KeyBuilderAPIKeyStrategy:
		// Empty follows the ePBS strategy.
		return requireOneOf(key, v, append([]string{""}, KeyStrategies...)...)

	case KeyEPBSBidCandidate:
		return requireOneOf(key, v, append([]string{"auto", "all"}, CandidateKeys...)...)

	case KeyBuilderAPIServeCandidates:
		policy, _ := v.(string)

		return ValidateServeCandidates(policy)

	case KeyEPBSHeadVoteThreshold, KeyRevealVoteThreshold, KeyBuildAutoWeakHeadPct,
		KeyBuilderAPIExecutionPaymentPct:
		if pct, _ := v.(uint64); pct > 100 {
			return fmt.Errorf("%s is a percentage (0-100), got %d", key, pct)
		}

	case KeyEPBSBidInterval, KeyRevealRetryInterval:
		if ms, _ := v.(int64); ms < 0 {
			return fmt.Errorf("%s must be >= 0, got %d", key, ms)
		}
	}

	return nil
}

// ValidateServeCandidates checks a serve-candidates policy string: all,
// canonical_only, or a comma-separated list of candidate keys.
func ValidateServeCandidates(policy string) error {
	switch policy {
	case "all", "canonical_only":
		return nil
	}

	for _, candidate := range strings.Split(policy, ",") {
		if !slices.Contains(CandidateKeys, strings.TrimSpace(candidate)) {
			return fmt.Errorf("serve_candidates must be all, canonical_only "+
				"or a comma-separated candidate key list, got %q", policy)
		}
	}

	return nil
}

func requirePositive(key string, v any) error {
	if n, _ := v.(uint64); n == 0 {
		return fmt.Errorf("%s must be greater than 0", key)
	}

	return nil
}

func requireOneOf(key string, v any, allowed ...string) error {
	value, ok := v.(string)
	if !ok || !slices.Contains(allowed, value) {
		return fmt.Errorf("%s must be one of %s, got %q", key, strings.Join(allowed, ", "), v)
	}

	return nil
}
