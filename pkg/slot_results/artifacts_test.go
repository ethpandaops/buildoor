package slot_results

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/buildoor/pkg/db"
)

func TestIsCustomPayloadArtifact(t *testing.T) {
	testRun := &db.SlotArtifact{Kind: ArtifactKindPayload, Meta: `{"build":"testing","run":2}`}
	engineBuild := &db.SlotArtifact{Kind: ArtifactKindPayload, Meta: `{"build":"engine"}`}
	custom := &db.SlotArtifact{Kind: ArtifactKindPayload, Meta: `{"source":"local"}`}
	engine := &db.SlotArtifact{Kind: ArtifactKindPayload, Meta: `{"source":"el"}`}
	legacy := &db.SlotArtifact{Kind: ArtifactKindPayload, Meta: `{}`}
	envelope := &db.SlotArtifact{Kind: ArtifactKindEnvelope, Meta: `{"source":"local"}`}

	require.True(t, IsCustomPayloadArtifact(testRun))
	require.False(t, IsCustomPayloadArtifact(engineBuild))
	require.True(t, IsCustomPayloadArtifact(custom))
	require.False(t, IsCustomPayloadArtifact(engine))
	require.False(t, IsCustomPayloadArtifact(legacy), "artifacts without a source are engine payloads")
	require.False(t, IsCustomPayloadArtifact(envelope))
	require.False(t, IsCustomPayloadArtifact(nil))
}
