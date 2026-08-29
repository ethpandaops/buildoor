package config

import (
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/buildoor/pkg/db"
)

// TestSnapshots_RaceFree drives SetMany (the real write path) against
// concurrent snapshot readers under the race detector. Published snapshots are
// immutable, so a reader must always observe a coherent settings generation:
// no torn string reads, and never a mix of two SetMany batches within one
// snapshot.
func TestSnapshots_RaceFree(t *testing.T) {
	store := db.NewDatabase(&db.Config{}, testLogger())
	require.NoError(t, store.Init())

	svc := boot(t, store, defaultsConfig(), nil)

	var wg sync.WaitGroup

	stop := make(chan struct{})

	// Writer: every batch sets the subsidy and the extra-data prefix to the
	// same sequence number, so a coherent snapshot always agrees on both.
	wg.Add(1)

	go func() {
		defer wg.Done()

		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}

			n := strconv.Itoa(i)
			err := svc.SetMany(map[string]json.RawMessage{
				KeyEPBSBidSubsidy: json.RawMessage(n),
				KeyExtraData:      json.RawMessage(`"gen-` + n + `"`),
			}, "race-test")
			if err != nil {
				t.Errorf("SetMany: %v", err)
				return
			}
		}
	}()

	for range 10_000 {
		cfg := svc.Current()

		// Snapshots taken before the first batch keep the defaults; once a
		// batch is visible, both fields must agree on the generation.
		if gen, ok := strings.CutPrefix(cfg.ExtraData, "gen-"); ok {
			if want := strconv.FormatUint(cfg.EPBS.BidSubsidy, 10); gen != want {
				t.Fatalf("torn snapshot: subsidy %d but extra data %q", cfg.EPBS.BidSubsidy, cfg.ExtraData)
			}
		}
	}

	close(stop)
	wg.Wait()
}
