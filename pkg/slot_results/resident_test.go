package slot_results

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOldRecordsKeepOnlySummariesInMemory(t *testing.T) {
	env := newTrackerTestEnv(t, true)
	tracker := env.tracker

	tracker.SetPersistence(t.Context(), env.stateDB)
	defer tracker.store.Stop()

	tracker.RecordBlockSubmission(2000, "epbs", string(SubmissionStatusAccepted), "")

	// Not yet persisted: the full record stays.
	tracker.compactBefore(2001)
	inMemory, _ := tracker.store.Get(2000)
	require.False(t, inMemory.resident, "an unpersisted change keeps the full record")

	require.NoError(t, tracker.store.Flush())
	tracker.compactBefore(2001)

	inMemory, _ = tracker.store.Get(2000)
	require.True(t, inMemory.resident)
	require.Nil(t, inMemory.AppliedPlan, "memory keeps the summary")
	require.Len(t, inMemory.BlockSubmissions, 1)

	full := tracker.Get(2000)
	require.NotNil(t, full.AppliedPlan, "reads get the full record back from the state-db")
	require.Len(t, tracker.GetRange(2000, 2000), 1)
	require.NotNil(t, tracker.GetRange(2000, 2000)[0].AppliedPlan)
	require.Nil(t, tracker.GetSummaries(2000, 2000)[0].AppliedPlan)

	// A late update mutates the full record, never the summary.
	tracker.RecordBlockSubmission(2000, "epbs", string(SubmissionStatusAccepted), "")

	full = tracker.Get(2000)
	require.NotNil(t, full.AppliedPlan)
	require.Len(t, full.BlockSubmissions, 2)

	inMemory, _ = tracker.store.Get(2000)
	require.False(t, inMemory.resident, "an updated record is full in memory again")

	require.NoError(t, tracker.store.Flush())

	stored, found, err := tracker.kv.Get(2000)
	require.NoError(t, err)
	require.True(t, found)
	require.NotNil(t, stored.AppliedPlan, "the state-db keeps the full record")
	require.Len(t, stored.BlockSubmissions, 2)
}

func TestWithoutStateDBRecordsStayFull(t *testing.T) {
	env := newTrackerTestEnv(t, false)
	tracker := env.tracker

	tracker.SetPersistence(t.Context(), env.stateDB)
	defer tracker.store.Stop()

	tracker.RecordBlockSubmission(2000, "epbs", string(SubmissionStatusAccepted), "")
	tracker.compactBefore(2001)

	inMemory, _ := tracker.store.Get(2000)
	require.False(t, inMemory.resident, "nowhere to read a full record back from")
	require.NotNil(t, tracker.Get(2000).AppliedPlan)
}
