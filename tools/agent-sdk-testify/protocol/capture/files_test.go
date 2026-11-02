package capture

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zoumo/agent-sdk-go/tools/agent-sdk-testify/protocol/recording"
)

func TestRawArtifactWriteFailureRetainsFailedEvidence(t *testing.T) {
	dir := t.TempDir()
	journal, err := openJournal(Options{Output: dir}, time.Now())
	require.NoError(t, err)
	// Close the recording descriptor to force a storage failure independently of
	// forwarding. A frame that cannot be recorded must never be forwarded as if
	// the capture were complete.
	require.NoError(t, journal.raw.Close())
	ordinal, err := journal.frame(recording.ClientToServer, []byte(`{"unknown":1e+09}`))
	require.Error(t, err)
	assert.EqualValues(t, 1, ordinal)
	assert.EqualValues(t, 0, journal.frames)
	report := recording.TransportReport{Version: 1, Reason: "transport-error", CleanupComplete: true}
	require.Error(t, journal.finish(&report, recording.NativeIdentity{}, err))
	var persisted recording.TransportReport
	data, err := os.ReadFile(filepath.Join(dir, "transport.json"))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &persisted))
	assert.Equal(t, "failed", persisted.CaptureStatus)
	assert.Contains(t, persisted.Error, "c2s frame 1")
	var manifest recording.Manifest
	data, err = os.ReadFile(filepath.Join(dir, "manifest.json"))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &manifest))
	assert.Equal(t, "failed", manifest.CaptureStatus)
	for name, hash := range manifest.Files {
		data, err = os.ReadFile(filepath.Join(dir, name))
		require.NoError(t, err)
		assert.Equal(t, hash, recording.Hash(data))
	}
}
