package official

import (
	"context"
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
	"github.com/zoumo/agent-sdk-testify/internal/tool"
	"github.com/zoumo/agent-sdk-testify/internal/version"
)

type VerifyOptions struct {
	FixtureDir string
	Timeout    time.Duration
	Index      string
	Scenarios  string
	Archive    string
}

// Verify owns the SDK driver, not an execution or a protocol environment.
func Verify(ctx context.Context, o VerifyOptions) (runErr error) {
	if !filepath.IsAbs(o.FixtureDir) {
		return errors.New("official verify requires absolute --fixture-dir")
	}
	if o.Timeout < 0 {
		return errors.New("timeout must be nonnegative")
	}
	if o.Index == "" && (o.Scenarios != "" || o.Archive != "") {
		return errors.New("scenarios and archive require index")
	}
	c := corpus.Corpus{Index: o.Index, Scenarios: o.Scenarios, Archive: o.Archive}
	if c.Index != "" {
		if c.Archive == "" {
			var err error
			c.Archive, err = corpus.DefaultArchive()
			if err != nil {
				return err
			}
		}
		if err := c.Normalize(); err != nil {
			return err
		}
	}
	if o.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, o.Timeout)
		defer cancel()
	}
	fixture, err := lifecycle.OpenFixture(o.FixtureDir)
	if err != nil {
		return err
	}
	var activity *lifecycle.OfficialRun
	verificationSaved := false
	evidence := tool.Object{"schemaVersion": 1, "fixtureId": fixture.FixtureID, "mode": "verify", "pipeline": "public-agent-mock", "status": "failed", "cleanupComplete": true, "startedAt": time.Now().UTC().Format(time.RFC3339Nano)}
	defer func() {
		if activity == nil {
			return
		}
		if !verificationSaved {
			evidence["completedAt"] = time.Now().UTC().Format(time.RFC3339Nano)
			if runErr == nil {
				evidence["status"] = "passed"
			} else {
				evidence["error"] = runErr.Error()
			}
			runErr = errors.Join(runErr, tool.WriteJSON(filepath.Join(activity.Dir, "verification.json"), evidence))
		}
		if verificationSaved && runErr != nil {
			evidence["status"] = "failed"
			evidence["error"] = runErr.Error()
		}
		runErr = errors.Join(runErr, activity.Complete(evidence))
	}()

	material, err := lifecycle.LatestArtifact(o.FixtureDir, fixture)
	if err != nil {
		return err
	}
	if material.Provenance != "official" && material.Provenance != "builtin" {
		return errors.New("material has no applicable official verifier: provenance must be official or builtin")
	}
	evidence["recording"], evidence["material"] = material.Path, material
	before, err := corpus.Inventory(material.Path, false)
	if err != nil {
		return err
	}
	evidence["sourceFiles"] = before
	if err := corpus.ValidateVerificationSource(*material, before); err != nil {
		return err
	}
	if c.Index != "" {
		evidence["admission"] = &lifecycle.Admission{Index: c.Index, Scenarios: c.Scenarios, Archive: c.Archive}
	}

	var plan tool.Object
	if err := tool.ReadJSON(filepath.Join(material.Path, "plan.json"), &plan); err != nil {
		return err
	}
	bound, err := catalog.Load(data.Scenarios(), catalog.Selection{Provider: fixture.AgentType, Case: tool.Str(plan["caseId"]), Model: tool.Str(plan["model"])})
	if err != nil {
		return err
	}
	if err := catalog.CheckVerifierPlan(plan, bound); err != nil {
		return err
	}
	evidence["case"], evidence["provider"], evidence["binding"] = plan["caseId"], fixture.AgentType, bound
	contracts := tool.Object{}
	for _, name := range []string{tool.Str(bound["storyId"]) + ".json", "bindings/" + fixture.AgentType + ".json"} {
		content, err := fs.ReadFile(data.Scenarios(), name)
		if err != nil {
			return err
		}
		contracts[name] = tool.DigestBytes(content)
	}
	evidence["contracts"] = contracts
	driver, err := ResolveDriver(fixture.AgentType)
	if err != nil {
		return err
	}
	driverHash, err := tool.FileDigest(driver)
	if err != nil {
		return err
	}
	evidence["driver"] = tool.Object{"path": driver, "sha256": driverHash}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	toolHash, err := tool.FileDigest(self)
	if err != nil {
		return err
	}
	evidence["tool"] = tool.Object{"version": version.String(), "sha256": toolHash}
	// Preconditions do not create orchestration facts. Registration then pins the
	// exact preflight input against concurrent record before a driver can start.
	activity, err = lifecycle.BeginOfficial(o.FixtureDir, "verify")
	if err != nil {
		return err
	}
	o.FixtureDir, fixture = activity.FixtureDir, activity.Fixture
	evidence["id"], evidence["output"] = activity.ID, activity.Dir
	pinned, err := lifecycle.LatestArtifact(o.FixtureDir, fixture)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(material, pinned) {
		return errors.New("current recording changed before verification")
	}
	inventory, err := corpus.Inventory(material.Path, false)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(before, inventory) {
		return errors.New("material changed before verification")
	}
	var index lifecycle.Index
	if err := tool.ReadJSON(lifecycle.IndexPath(o.FixtureDir), &index); err != nil {
		return err
	}
	previous := map[string]bool{}
	for _, execution := range index.Executions {
		previous[execution.ID] = true
	}
	evidence["priorExecutions"] = previous
	root := filepath.Join(activity.Dir, "driver")
	route := fixture.Routes["mock"]
	env := route.Env
	plan["root"], plan["home"], plan["config"], plan["raw"] = root, env["HOME"], env[map[string]string{"codex": "CODEX_HOME", "claude": "CLAUDE_CONFIG_DIR"}[fixture.AgentType]], filepath.Join(material.Path, "raw")
	plan["_seedRoot"] = filepath.Dir(route.Workspace)
	plan["_recordedWorkspace"] = plan["_fixtureProject"]
	plan["_fixtureProject"], plan["_replay"] = route.Workspace, true
	// project/historyProject retain the original native protocol cwd. Only the
	// fixture and SDK host use the ordinary mock route slots.
	private := filepath.Join(fixture.ResourceRoot, "official", activity.ID)
	peers := make(map[string]tool.Command, len(material.Connections))
	for _, id := range material.Connections {
		args := []string{"agent", "mock", "--fixture-dir", o.FixtureDir, "--connection", id}
		if o.Timeout > 0 {
			args = append(args, "--timeout", fmt.Sprint(o.Timeout.Seconds()))
		}
		peers[id] = tool.Command{Command: self, Args: args}
	}
	var recordEvidenceErr error
	run, driverErr := runDriver(ctx, driverRunOptions{
		Activity:   activity,
		Driver:     driver,
		Env:        env,
		Workspace:  route.Workspace,
		Root:       root,
		PrivateDir: private,
		Plan:       plan,
		Peers:      peers,
		ReportFile: "replay-report.json",
		Inspect: func(outcome, report tool.Object, priorErr error) error {
			var inspectErr error
			inspectErr = errors.Join(inspectErr, validateBusinessEvidence(fixture.AgentType, plan, bound, outcome, report))
			refs, refErr := mockReferences(o.FixtureDir, fixture, root, material.Connections, *material, previous)
			evidence["connections"] = refs
			inspectErr = errors.Join(inspectErr, refErr)
			after, err := corpus.Inventory(material.Path, false)
			inspectErr = errors.Join(inspectErr, err)
			if !reflect.DeepEqual(before, after) {
				inspectErr = errors.Join(inspectErr, errors.New("material changed during verification"))
			}
			inspectErr = errors.Join(inspectErr, snapshotMocks(o.FixtureDir, activity.Dir, refs), activity.RestoreMockRoute())
			if c.Index != "" && material.OfficialOrigin != nil && errors.Join(priorErr, inspectErr) == nil {
				target := filepath.Join(root, "record-origin")
				recordEvidenceErr = lifecycle.SnapshotOfficialRecord(o.FixtureDir, fixture, *material, target)
				if recordEvidenceErr == nil {
					files, hashErr := corpus.Inventory(target, false)
					recordEvidenceErr = hashErr
					evidence["officialRecord"] = tool.Object{"root": "driver/record-origin", "files": files}
				}
			}
			return inspectErr
		},
	})
	runErr = driverErr
	if run == nil {
		return runErr
	}
	evidence["driverProcess"] = run.Process
	if run.ProcessGroupFailed {
		evidence["cleanupComplete"] = false
	}
	evidence["business"], evidence["report"], evidence["officialSDK"] = run.Outcome, run.Report, run.Report["officialSDK"]
	evidence["driverFiles"] = run.Files
	evidence["files"] = tool.Object{}
	for name, hash := range run.Files {
		tool.Obj(evidence["files"])["driver/"+name] = hash
	}
	// Preserve successful verification independently of an optional admission.
	if runErr == nil {
		evidence["status"] = "passed"
	}
	evidence["completedAt"] = time.Now().UTC().Format(time.RFC3339Nano)
	if runErr != nil {
		evidence["error"] = runErr.Error()
	}
	if err := tool.WriteJSON(filepath.Join(activity.Dir, "verification.json"), evidence); err != nil {
		return errors.Join(runErr, err)
	}
	verificationSaved = true
	if c.Index != "" {
		admissionErr := runErr
		if admissionErr == nil {
			admissionErr = recordEvidenceErr
		}
		admissionErr = admitVerified(material.Path, tool.Object{"output": activity.Dir}, admissionErr, c)
		if admissionErr != nil {
			evidence["admissionError"] = admissionErr.Error()
		}
		// Admission failure must not overwrite the protocol/business conclusion.
		if admissionErr != nil {
			return admissionErr
		}
	}
	return runErr
}

func validateBusinessEvidence(agent string, plan, bound, outcome, report tool.Object) error {
	sdk := tool.Obj(report["officialSDK"])
	if outcome["status"] != "passed" || report["status"] != "passed" || report["branchStatus"] != "passed" || report["captureStatus"] != "complete" || len(tool.Array(report["gaps"])) != 0 || report["caseId"] != plan["caseId"] || report["storyId"] != bound["storyId"] || report["provider"] != agent || sdk["package"] != map[string]string{"codex": "openai-codex", "claude": "claude-agent-sdk"}[agent] || tool.Str(sdk["version"]) == "" {
		return errors.New("official SDK business evidence is absent, failed or untriggered")
	}
	return nil
}
