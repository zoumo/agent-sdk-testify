package lifecycle

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/agent-sdk-testify/internal/history"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

func TestOfficialSettlementRecoveryIsExactAndOpenIsReadOnly(t *testing.T) {
	for _, fault := range []string{"before-rename", "after-rename", "both-copies", "hash-conflict", "unresolved-group", "unsealed", "unknown-mode", "wrong-envelope", "wrong-execution", "verify-without-material"} {
		t.Run(fault, func(t *testing.T) {
			root := prepareRecordTransactionFixture(t)
			run, err := BeginOfficial(root, "record")
			require.NoError(t, err)
			root = run.FixtureDir
			journal, err := readOfficialJournal(root)
			require.NoError(t, err)
			prefix := "failures/official-record/" + run.ID
			target := filepath.Join(root, filepath.FromSlash(prefix))
			require.NoError(t, os.WriteFile(filepath.Join(run.Dir, "stdout.log"), nil, 0o600))
			require.NoError(t, os.WriteFile(filepath.Join(run.Dir, "stderr.log"), []byte("synthetic failure\n"), 0o600))
			files, err := history.HashTree(run.Dir)
			require.NoError(t, err)
			inventory := tool.Object{}
			for name, hash := range files {
				inventory[prefix+"/"+name] = hash
			}
			result := tool.Object{"schemaVersion": 3, "id": run.ID, "fixtureId": run.Fixture.FixtureID, "mode": "record", "status": "failed", "cleanupComplete": true, "files": inventory}
			require.NoError(t, writeExclusive(filepath.Join(run.Dir, "result.json"), result))
			hash, err := tool.FileDigest(filepath.Join(run.Dir, "result.json"))
			require.NoError(t, err)
			journal["status"], journal["target"], journal["envelope"], journal["sha256"] = "settling", prefix, "result.json", hash
			journal["processes"] = tool.Object{"driver": tool.Object{"state": "stopped", "pid": 123}}
			switch fault {
			case "after-rename":
				require.NoError(t, os.MkdirAll(filepath.Dir(target), 0o700))
				require.NoError(t, os.Rename(run.Dir, target))
			case "both-copies":
				require.NoError(t, tool.CopyTree(run.Dir, target))
			case "hash-conflict":
				require.NoError(t, os.WriteFile(filepath.Join(run.Dir, "stderr.log"), []byte("changed"), 0o600))
			case "unresolved-group":
				journal["processes"] = tool.Object{"driver": tool.Object{"state": "started", "pid": 99999999}}
			case "unsealed":
				journal["status"] = "running"
			case "unknown-mode":
				journal["mode"] = "unknown"
			case "wrong-envelope":
				journal["envelope"] = "verification.json"
			case "wrong-execution":
				journal["executionId"] = "other"
			case "verify-without-material":
				journal["mode"] = "verify"
			}
			require.NoError(t, writeAtomic(filepath.Join(root, "official.json"), journal))
			require.NoError(t, run.lock.Close())
			before, err := journalFiles(root)
			require.NoError(t, err)
			_, err = OpenFixture(root)
			require.NoError(t, err)
			after, err := journalFiles(root)
			require.NoError(t, err)
			require.Equal(t, before, after)
			next, err := BeginOfficial(root, "record")
			if fault == "before-rename" || fault == "after-rename" {
				require.NoError(t, err)
				require.NotEqual(t, run.ID, next.ID)
				require.NoDirExists(t, run.Dir)
				require.FileExists(t, filepath.Join(target, "result.json"))
				require.Equal(t, hash, mustDigest(t, filepath.Join(target, "result.json")))
				require.NoError(t, next.Complete(tool.Object{"schemaVersion": 3, "id": next.ID, "fixtureId": next.Fixture.FixtureID, "mode": "record", "status": "failed", "cleanupComplete": true}))
				require.NoFileExists(t, filepath.Join(root, "official.json"))
			} else {
				require.Error(t, err)
				require.FileExists(t, filepath.Join(root, "official.json"))
				current, err := journalFiles(root)
				require.NoError(t, err)
				require.Equal(t, before, current)
			}
			t.Logf("fault=%s journalRetained=%t staging=%s target=%s", fault, next == nil, run.Dir, target)
		})
	}
}

func mustDigest(t *testing.T, path string) string {
	t.Helper()
	hash, err := tool.FileDigest(path)
	require.NoError(t, err)
	return hash
}

func TestOfficialClaimBeforeNotificationCannotBecomeNoMaterial(t *testing.T) {
	root := prepareRecordTransactionFixture(t)
	run, err := BeginOfficial(root, "record")
	require.NoError(t, err)
	lock, err := actionLock(run.FixtureDir, true)
	require.NoError(t, err)
	require.NoError(t, bindOfficialExecutionLocked(run.FixtureDir, run.ID, "reserved-material", "reserved-execution"))
	require.NoError(t, lock.Close())
	err = run.Complete(tool.Object{"schemaVersion": 3, "id": run.ID, "fixtureId": run.Fixture.FixtureID, "mode": "record", "status": "failed", "cleanupComplete": true})
	require.ErrorContains(t, err, "has not sealed")
	require.DirExists(t, run.Dir)
	require.NoDirExists(t, filepath.Join(run.FixtureDir, "failures", "official-record", run.ID))
	journal, err := readOfficialJournal(run.FixtureDir)
	require.NoError(t, err)
	require.Equal(t, "reserved-execution", journal["executionId"])
	require.Equal(t, "reserved-material", journal["materialId"])
}

func journalFiles(root string) (map[string]string, error) {
	files := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		hash, err := tool.FileDigest(path)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(relative)] = hash
		return nil
	})
	return files, err
}
