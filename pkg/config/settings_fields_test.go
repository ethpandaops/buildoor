package config

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestLockedStringFields_RaceFree guards against NM-02: a UI override
// (SetMany/recompute, via Field.Set) racing a live hot-path read of the same
// string setting. Before the fix, Field.Set mutated the field in place with
// no lock while every reader dereferenced it unsynchronized — a torn
// {ptr,len} read could produce an out-of-bounds slice panic, and the field
// was reachable by any unauthenticated client on --api-port (the Builder
// API's port). This drives Field.Set (the exact path SetMany/recompute use)
// against each field's real accessor method concurrently under -race: run
// with `go test -race` (the default for this repo's CI), any surviving race
// fails the test.
func TestLockedStringFields_RaceFree(t *testing.T) {
	cfg := &Config{}
	fields := Fields()
	byKey := make(map[string]Field, len(fields))

	for _, f := range fields {
		byKey[f.Key] = f
	}

	// One entry per locked string field: the settings key and the real
	// accessor method a hot-path reader actually calls.
	cases := []struct {
		name   string
		key    string
		reader func() string
	}{
		{"ExtraData", KeyExtraData, cfg.GetExtraData},
		{"EPBS.BidCandidate", KeyEPBSBidCandidate, cfg.EPBS.GetBidCandidate},
		{"BuilderAPI.ServeCandidates", KeyBuilderAPIServeCandidates, cfg.BuilderAPI.GetServeCandidates},
		{"Reveal.GateMode", KeyRevealGateMode, cfg.Reveal.NormalizedGateMode},
		{"Reveal.BroadcastValidation", KeyRevealBroadcastValidation, cfg.Reveal.NormalizedBroadcastValidation},
		{"Build.CandidateParentFull", KeyBuildCandidateParentFull, func() string { return cfg.Build.CandidateMode("parent_full") }},
		{"Build.CandidateParentEmpty", KeyBuildCandidateParentEmpty, func() string { return cfg.Build.CandidateMode("parent_empty") }},
		{"Build.CandidateGrandparentFull", KeyBuildCandidateGrandparentFull, func() string { return cfg.Build.CandidateMode("grandparent_full") }},
		{"Build.CandidateGrandparentEmpty", KeyBuildCandidateGrandparentEmpty, func() string { return cfg.Build.CandidateMode("grandparent_empty") }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			field, ok := byKey[tc.key]
			require.True(t, ok, "field %q must be registered", tc.key)

			var wg sync.WaitGroup

			stop := make(chan struct{})

			wg.Add(1)

			go func() {
				defer wg.Done()

				i := 0
				for {
					select {
					case <-stop:
						return
					default:
					}

					i++
					_ = field.Set(cfg, "value-"+string(rune('a'+i%26)))
				}
			}()

			deadline := time.Now().Add(50 * time.Millisecond)
			for time.Now().Before(deadline) {
				_ = tc.reader()
			}

			close(stop)
			wg.Wait()
		})
	}
}
