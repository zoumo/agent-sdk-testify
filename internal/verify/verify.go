package verify

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"github.com/zoumo/agent-sdk-testify/data"
	"github.com/zoumo/agent-sdk-testify/internal/catalog"
	"github.com/zoumo/agent-sdk-testify/internal/corpus"
	"github.com/zoumo/agent-sdk-testify/internal/lifecycle"
	"github.com/zoumo/agent-sdk-testify/internal/official"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
	"github.com/zoumo/agent-sdk-testify/internal/version"
)

type Options struct {
	SessionDir string
	Timeout    time.Duration
	Index      string
	Scenarios  string
	Archive    string
}

func Run(ctx context.Context, o Options) error {
	c := corpus.Corpus{Index: o.Index, Scenarios: o.Scenarios, Archive: o.Archive}
	if o.SessionDir == "" {
		return errors.New("verify requires --session-dir")
	}
	if o.Timeout < 0 {
		return errors.New("timeout must be nonnegative")
	}
	if c.Index == "" && c.Scenarios != "" {
		return errors.New("scenarios requires index")
	}
	if c.Index == "" && c.Archive != "" {
		return errors.New("archive requires index")
	}
	var admission *lifecycle.Admission
	if c.Index != "" {
		if c.Archive == "" {
			archive, err := corpus.DefaultArchive()
			if err != nil {
				return err
			}
			c.Archive = archive
		}
		if err := c.Normalize(); err != nil {
			return err
		}
		admission = &lifecycle.Admission{Index: c.Index, Scenarios: c.Scenarios, Archive: c.Archive}
	}
	return verifyPrepared(ctx, o.SessionDir, o.Timeout, c, admission)
}

func verifyPrepared(ctx context.Context, sessionDir string, timeout time.Duration, c corpus.Corpus, admission *lifecycle.Admission) (err error) {
	if !filepath.IsAbs(sessionDir) {
		return errors.New("--session-dir must be an absolute path")
	}
	session, err := lifecycle.OpenActionSession(sessionDir)
	if err != nil {
		return err
	}
	artifact, err := lifecycle.LatestArtifact(sessionDir, session, true)
	if err != nil {
		return err
	}
	args := []string{"--session-dir", sessionDir}
	if admission != nil {
		args = append(args,
			"--index", admission.Index,
			"--scenarios", admission.Scenarios,
			"--archive", admission.Archive,
		)
	}
	driver, err := official.ResolveDriver(session.AgentType)
	if err != nil {
		return err
	}
	run, err := lifecycle.BeginVerify(sessionDir, args, timeout, artifact, admission)
	if err != nil {
		return err
	}
	status := "failed"
	defer func() {
		err = errors.Join(err, lifecycle.FinishAction(run, status, ""), run.Close())
	}()
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	o := official.Options{Mode: "verify", Materials: artifact.Path, HistoryPrepared: true, Driver: driver}
	summary, verifyErr := verifyRecordingAt(ctx, o, admission, run.OverlayRoot, run.Execution.Workspace, run)
	// Persist the exact verification before attempting any repository write.
	verifyErr = errors.Join(verifyErr, snapshotVerification(run.OverlayRoot, run.ArtifactRoot))
	err = admitVerified(artifact.Path, summary, verifyErr, c)
	if snapshotErr := snapshotVerification(run.OverlayRoot, run.ArtifactRoot); snapshotErr != nil {
		err = errors.Join(err, snapshotErr)
	}
	if summary != nil {
		b, encodeErr := json.Marshal(summary)
		if encodeErr != nil {
			err = errors.Join(err, fmt.Errorf("encode verify summary: %w", encodeErr))
		} else if _, encodeErr = fmt.Fprintln(os.Stdout, string(b)); encodeErr != nil {
			err = errors.Join(err, encodeErr)
		}
	}
	if err == nil {
		status = "passed"
	}
	return err
}

func admitVerified(materials string, summary tool.Object, err error, c corpus.Corpus) error {
	if summary == nil || c.Index == "" {
		return err
	}
	root := tool.Str(summary["output"])
	admission := tool.Object{"status": "rejected"}
	if err == nil {
		var verification tool.Object
		err = tool.ReadJSON(filepath.Join(root, "verification.json"), &verification)
		if err == nil {
			var files tool.Object
			files, err = corpus.Inventory(materials, false)
			if err == nil && (verification["status"] != "passed" || !reflect.DeepEqual(files, verification["sourceFiles"])) {
				err = errors.New("admission requires the exact verified material")
			}
		}
	}
	if err == nil {
		var review tool.Object
		review, err = c.Qualification(materials, root, false)
		reviewPath := filepath.Join(root, "qualification.json")
		if err == nil {
			err = corpus.Write(reviewPath, review)
		}
		if err == nil {
			var target string
			target, err = c.Adopt(materials, root, reviewPath)
			admission["materials"] = target
			if err == nil {
				admission["status"] = "admitted"
			}
		}
	}
	if err != nil {
		admission["error"] = err.Error()
	}
	return errors.Join(err, tool.WriteJSON(filepath.Join(root, "admission-summary.json"), admission))
}

func snapshotVerification(source, target string) error {
	files := []string{
		"plan.json",
		"manifest.json",
		"report.json",
		"result.json",
		"summary.json",
		"verification.json",
		"replay-report.json",
		"replay-peer.json",
		"remote-probe/replay-peer.json",
		"peer-main.json",
		"peer-probe.json",
		"peer-main-lifecycle.json",
		"peer-probe-lifecycle.json",
		"remote-probe/peer-probe-lifecycle.json",
		"materialization.json",
		"qualification.json",
		"admission-summary.json",
		"remote-service.json",
		"source-report.json",
		"stderr.log",
		"stdout.log",
	}
	for _, name := range files {
		from := filepath.Join(source, filepath.FromSlash(name))
		info, err := os.Lstat(from)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("verification evidence is not regular: %s", name)
		}
		if err := tool.CopyFile(from, filepath.Join(target, filepath.FromSlash(name))); err != nil {
			return err
		}
	}
	return nil
}

func verifyRecordingAt(ctx context.Context, o official.Options, admission *lifecycle.Admission, preparedOutput, preparedWorkspace string, run *lifecycle.ActionRun) (summary tool.Object, err error) {
	execution := run.Execution
	o.Mode = "verify"
	o.Materials, err = corpus.Resolved(o.Materials)
	if err != nil {
		return nil, err
	}
	if preparedOutput == "" {
		return nil, errors.New("verify requires a prepared execution output")
	}
	{
		resolved, e := corpus.Resolved(preparedOutput)
		if e != nil {
			return nil, e
		}
		if resolved == o.Materials || corpus.Within(resolved, o.Materials) || corpus.Within(o.Materials, resolved) {
			return nil, errors.New("verification output must be separate from recording")
		}
	}
	o.Output = preparedOutput
	o.Workspace = preparedWorkspace
	o.OutputPrepared = true
	o.Output, err = filepath.EvalSymlinks(o.Output)
	if err != nil {
		return nil, err
	}
	o.OutputPrepared = true
	evidence := tool.Object{"schemaVersion": 1, "status": "failed", "recording": o.Materials, "output": o.Output}
	if admission != nil {
		evidence["admission"] = admission
	}
	defer func() {
		err = errors.Join(err, lifecycle.CleanupAction(run))
		if summary == nil {
			summary = tool.Object{"mode": "verify", "output": o.Output, "result": tool.Result{Status: "failed"}}
		}
		if err != nil {
			summary["error"] = err.Error()
			evidence["error"] = err.Error()
			evidence["status"] = "failed"
		}
		err = errors.Join(err, tool.WriteJSON(filepath.Join(o.Output, "summary.json"), summary))
		// Bind the final run evidence, including failures, without a hash cycle.
		hashes := tool.Object{}
		for _, name := range []string{"summary.json", "result.json", "replay-report.json", "replay-peer.json", "remote-probe/replay-peer.json", "materialization.json", "peer-main-lifecycle.json", "peer-probe-lifecycle.json"} {
			if digest, e := tool.FileDigest(filepath.Join(o.Output, name)); e == nil {
				hashes[name] = digest
			}
		}
		evidence["files"] = hashes
		err = errors.Join(err, tool.WriteJSON(filepath.Join(o.Output, "verification.json"), evidence))
	}()
	self, e := os.Executable()
	if e != nil {
		return nil, e
	}
	digest, e := tool.FileDigest(self)
	if e != nil {
		return nil, e
	}
	evidence["tool"] = tool.Object{"version": version.Version, "sha256": digest}
	before, e := corpus.Inventory(o.Materials, false)
	if e != nil {
		return nil, e
	}
	evidence["sourceFiles"] = before
	materialBytes, e := json.Marshal(before)
	if e != nil {
		return nil, e
	}
	evidence["material"] = tool.Object{"id": execution.Artifact.ID, "sha256": tool.DigestBytes(materialBytes), "files": before, "provenance": execution.Artifact.Provenance}
	evidence["sessionId"], evidence["executionId"] = execution.SessionID, execution.ExecutionID
	descriptorHash, e := tool.FileDigest(filepath.Join(filepath.Dir(execution.ArtifactRoot), "execution.json"))
	if e != nil {
		return nil, e
	}
	evidence["executionSHA256"] = descriptorHash
	evidence["connections"] = execution.Connections
	if before["adoption.json"] != nil {
		v := &corpus.Validation{}
		adoption := v.Read(o.Materials, "adoption.json")
		files := tool.Object{}
		for name, digest := range before {
			if name != "adoption.json" && name != "material.json" {
				files[name] = digest
			}
		}
		v.Check(reflect.DeepEqual(files, adoption["files"]), "adopted corpus inventory drift")
		review := v.Read(o.Materials, "review.json")
		v.Check(reflect.DeepEqual(review["captureFiles"], adoption["originalFiles"]), "original provenance hash inventory drift")
		if v.Err != nil {
			return nil, v.Err
		}
	}
	for _, name := range corpus.SourceEvidence {
		if before[name] == nil {
			return nil, fmt.Errorf("recording omits %s", name)
		}
	}
	// Missing manifests cannot turn an unbound raw file into a recording.
	var originalPlan tool.Object
	if planErr := tool.ReadJSON(filepath.Join(o.Materials, "plan.json"), &originalPlan); planErr != nil {
		return nil, planErr
	}
	vSource := &corpus.Validation{}
	manifest := vSource.Read(o.Materials, "manifest.json")
	vSource.Check(manifest["planSHA256"] == before["plan.json"], "source plan hash differs")
	for _, phase := range corpus.Phases(originalPlan) {
		rawRoot := filepath.Join(o.Materials, phase[1], "raw")
		m := vSource.Read(rawRoot, "manifest.json")
		vSource.Hashes(rawRoot, tool.Obj(m["files"]))
		for _, name := range []string{"raw.jsonl", "transport.json", "stderr.raw"} {
			vSource.Check(tool.Obj(m["files"])[name] != nil, "raw manifest omits required artifact: "+name)
		}
	}
	if vSource.Err != nil {
		return nil, vSource.Err
	}
	plan, e := loadMaterials(o)
	if e != nil {
		return nil, e
	}
	o.Provider, o.Case = tool.Str(plan["provider"]), tool.Str(plan["caseId"])
	evidence["provider"], evidence["case"] = o.Provider, o.Case
	catalogPlan, e := catalog.Load(data.Scenarios(), catalog.Selection{Provider: o.Provider, Case: o.Case, Model: o.Model})
	if e != nil {
		return nil, e
	}
	if contractErr := catalog.CheckVerifierPlan(plan, catalogPlan); contractErr != nil {
		return nil, contractErr
	}
	contracts := tool.Object{}
	for _, name := range []string{tool.Str(catalogPlan["storyId"]) + ".json", "bindings/" + o.Provider + ".json"} {
		content, readErr := fs.ReadFile(data.Scenarios(), name)
		if readErr != nil {
			return nil, readErr
		}
		hash := sha256.Sum256(content)
		contracts[name] = hex.EncodeToString(hash[:])
	}
	evidence["contracts"] = contracts
	digest, e = tool.FileDigest(o.Driver)
	if e != nil {
		return nil, e
	}
	evidence["driver"] = tool.Object{"path": o.Driver, "sha256": digest}
	summary, err = official.Run(ctx, o)
	var business tool.Object
	businessErr := tool.ReadJSON(filepath.Join(o.Output, "replay-report.json"), &business)
	evidence["officialSDK"] = business["officialSDK"]
	if summary != nil {
		evidence["driverProcess"] = summary["driverProcess"]
	}
	sdk := tool.Obj(business["officialSDK"])
	packageName := map[string]string{"codex": "openai-codex", "claude": "claude-agent-sdk"}[o.Provider]
	if businessErr == nil && (business["status"] != "passed" || business["branchStatus"] != "passed" || business["captureStatus"] != "complete" || len(tool.Array(business["gaps"])) != 0 || business["provider"] != plan["provider"] || business["caseId"] != plan["caseId"] || business["storyId"] != catalogPlan["storyId"] || sdk["package"] != packageName || tool.Str(sdk["version"]) == "") {
		businessErr = errors.New("official SDK business evidence is absent or incomplete")
	}
	v := &corpus.Validation{}
	v.Replay(o.Materials, o.Output, plan, "verify")
	after, sourceErr := corpus.Inventory(o.Materials, false)
	if sourceErr == nil && !reflect.DeepEqual(before, after) {
		sourceErr = errors.New("source changed during verification")
	}
	err = errors.Join(err, businessErr, v.Err, sourceErr)
	if err != nil {
		return summary, err
	}
	evidence["status"] = "passed"
	return summary, nil
}
