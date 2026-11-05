package conformance

import (
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestVerifyCurrentEvidenceAndAdmissionRetry(t *testing.T) {
	root := t.TempDir()
	agent, drivers, env := isolatedCommands(t, root, "codex")
	session := prepareSession(t, env, "--agent", "codex", "--case", "SDK-CASE-001")
	installDriver(t, drivers, "codex", "record")
	mustRun(t, env, t.TempDir(), "", "official", "record", "--session-dir", session, "--cmd", agent, "--", "app-server", "--listen", "stdio://")
	material := latestCapture(t, session)
	current := readFile(t, filepath.Join(session, "current.json"))
	materialBytes := readAllFiles(t, str(material["path"]))
	corpus := newSyntheticCorpus(t)
	index := readFile(t, corpus.index)
	installDriver(t, drivers, "codex", "verify")
	descriptor := readJSON(t, filepath.Join(session, "session.json"))
	sourcePlan := str(obj(descriptor["source"])["path"])
	sourceBytes := readFile(t, sourcePlan)
	require.NoError(t, os.Remove(sourcePlan))
	mustRun(t, env, t.TempDir(), "", "official", "verify", "--session-dir", session)
	require.NoError(t, os.WriteFile(sourcePlan, sourceBytes, 0o600))
	evidenceRoot, evidence := latestOfficialVerification(t, session)
	require.Equal(t, "passed", evidence["status"])
	require.NotContains(t, evidence, "admission")
	require.NoFileExists(t, filepath.Join(evidenceRoot, "admission-summary.json"))
	require.Equal(t, index, readFile(t, corpus.index))
	require.Equal(t, material["id"], obj(evidence["material"])["id"])
	require.Equal(t, material["provenance"], obj(evidence["material"])["provenance"])
	require.Equal(t, descriptor["sessionId"], evidence["sessionId"])
	refs := array(evidence["connections"])
	require.Len(t, refs, 1)
	ref := obj(refs[0])
	require.Equal(t, digestFile(t, filepath.Join(session, "executions", str(ref["executionId"]), "execution.json")), ref["executionSHA256"])
	files := obj(evidence["sourceFiles"])
	require.Contains(t, files, "material.json")
	require.Contains(t, files, "config/sessions/conformance-history.jsonl")
	assertFileHashes(t, str(material["path"]), files)
	assertFileHashes(t, evidenceRoot, obj(evidence["files"]))
	require.NotEmpty(t, obj(evidence["tool"])["sha256"])
	require.NotEmpty(t, obj(evidence["driver"])["sha256"])
	require.Equal(t, "conformance-test-only", obj(evidence["officialSDK"])["version"])
	require.NotEmpty(t, evidence["contracts"])
	// The index lock fails adoption after successful verification and archival.
	lock := strings.TrimSuffix(corpus.index, filepath.Ext(corpus.index)) + ".lock"
	require.NoError(t, os.WriteFile(lock, nil, 0o600))
	args := []string{"official", "verify", "--session-dir", session, "--index", corpus.index, "--scenarios", corpus.scenarios, "--archive", corpus.archive}
	failed := runTool(t, env, t.TempDir(), "", args...)
	require.NotZero(t, failed.code)
	failedRoot := latestVerificationRoot(t, session)
	require.Equal(t, "passed", readJSON(t, filepath.Join(failedRoot, "verification.json"))["status"])
	require.Equal(t, "rejected", readJSON(t, filepath.Join(failedRoot, "admission-summary.json"))["status"])
	failedBytes := readAllFiles(t, failedRoot)
	require.Equal(t, index, readFile(t, corpus.index))
	require.Equal(t, current, readFile(t, filepath.Join(session, "current.json")))
	require.Equal(t, materialBytes, readAllFiles(t, str(material["path"])))
	require.NoError(t, os.Remove(lock))
	mustRun(t, env, t.TempDir(), "", args...)
	admitted := readJSON(t, filepath.Join(latestVerificationRoot(t, session), "admission-summary.json"))
	require.Equal(t, "admitted", admitted["status"])
	adopted := readAllFiles(t, str(admitted["materials"]))
	// A failed replacement also keeps the old index and admitted bytes intact.
	admittedIndex := readFile(t, corpus.index)
	require.NoError(t, os.WriteFile(lock, nil, 0o600))
	failed = runTool(t, env, t.TempDir(), "", args...)
	require.NotZero(t, failed.code)
	require.Equal(t, admittedIndex, readFile(t, corpus.index))
	require.Equal(t, adopted, readAllFiles(t, str(admitted["materials"])))
	require.NoError(t, os.Remove(lock))
	mustRun(t, env, t.TempDir(), "", args...)
	retried := readJSON(t, filepath.Join(latestVerificationRoot(t, session), "admission-summary.json"))
	require.Equal(t, "admitted", retried["status"])
	require.DirExists(t, str(retried["materials"]))
	require.Equal(t, failedBytes, readAllFiles(t, failedRoot))
	require.Equal(t, current, readFile(t, filepath.Join(session, "current.json")))
	require.Equal(t, materialBytes, readAllFiles(t, str(material["path"])))
}

func TestVerifyPrerequisitesBeforeExecution(t *testing.T) {
	for _, fault := range []string{"external", "claimed-official", "wrong-story", "wrong-binding", "unsupported", "no-driver"} {
		t.Run(fault, func(t *testing.T) {
			root := t.TempDir()
			agent, drivers, env := isolatedCommands(t, root, "codex")
			producer := prepareSession(t, env, "--agent", "codex", "--case", "SDK-CASE-001")
			installDriver(t, drivers, "codex", "record")
			mustRun(t, env, t.TempDir(), "", "official", "record", "--session-dir", producer, "--cmd", agent, "--", "app-server", "--listen", "stdio://")
			material := str(latestCapture(t, producer)["path"])
			require.NoError(t, os.Remove(filepath.Join(material, "material.json")))
			planPath, manifestPath := filepath.Join(material, "plan.json"), filepath.Join(material, "manifest.json")
			plan, manifest := readJSON(t, planPath), readJSON(t, manifestPath)
			if fault != "external" {
				manifest["provenance"] = "official"
			}
			switch fault {
			case "claimed-official":
				plan["caseId"] = "consumer-defined"
			case "wrong-story":
				plan["storyId"] = "SDK-US-008"
			case "wrong-binding":
				obj(plan["scenario"])["prompts"] = []string{"altered prompt"}
			case "unsupported":
				source := prepareSession(t, env, "--agent", "codex", "--case", "SDK-CASE-003")
				descriptor := readJSON(t, filepath.Join(source, "session.json"))
				bound := readJSON(t, str(obj(descriptor["source"])["path"]))
				maps.Copy(plan, bound)
			}
			writeJSON(t, planPath, plan)
			manifest["planSHA256"] = digestFile(t, planPath)
			writeJSON(t, manifestPath, manifest)
			session := prepareSession(t, env, "--recording", material)
			installDriver(t, drivers, "codex", "verify")
			if fault == "no-driver" {
				require.NoError(t, os.Remove(filepath.Join(drivers, "agent-sdk-testify-codex")))
				env[0] = "PATH=/usr/bin:/bin"
			}
			require.NoError(t, os.RemoveAll(producer))
			before := readAllFiles(t, session)
			failed := runTool(t, env, t.TempDir(), "", "official", "verify", "--session-dir", session)
			require.NotZero(t, failed.code)
			require.Empty(t, array(sessionIndex(t, session)["executions"]))
			require.Equal(t, before, readAllFiles(t, session))
			entries, err := os.ReadDir(filepath.Join(session, "executions"))
			require.NoError(t, err)
			require.Len(t, entries, 1)
		})
	}
}

func TestVerifyCleanupFailureCannotBeAdopted(t *testing.T) {
	root := t.TempDir()
	agent, drivers, env := isolatedCommands(t, root, "codex")
	session := prepareSession(t, env, "--agent", "codex", "--case", "SDK-CASE-001")
	installDriver(t, drivers, "codex", "record")
	mustRun(t, env, t.TempDir(), "", "official", "record", "--session-dir", session, "--cmd", agent, "--", "app-server", "--listen", "stdio://")
	current := readFile(t, filepath.Join(session, "current.json"))
	corpus := newSyntheticCorpus(t)
	index := readFile(t, corpus.index)
	installDriver(t, drivers, "codex", "verify-cleanup-failure")
	result := runTool(t, env, t.TempDir(), "", "official", "verify", "--session-dir", session, "--index", corpus.index, "--scenarios", corpus.scenarios, "--archive", corpus.archive)
	require.NotZero(t, result.code)
	evidenceRoot := latestVerificationRoot(t, session)
	evidence := readJSON(t, filepath.Join(evidenceRoot, "verification.json"))
	require.Equal(t, "failed", evidence["status"])
	require.NotEmpty(t, evidence["error"])
	require.Equal(t, "rejected", readJSON(t, filepath.Join(evidenceRoot, "admission-summary.json"))["status"])
	require.Equal(t, index, readFile(t, corpus.index))
	require.Equal(t, current, readFile(t, filepath.Join(session, "current.json")))
	require.NoFileExists(t, filepath.Join(evidenceRoot, "qualification.json"))
	descriptor := readJSON(t, filepath.Join(session, "session.json"))
	require.NoError(t, os.Remove(filepath.Join(str(descriptor["resourceRoot"]), "routes", "mock", "workspace")))
}
