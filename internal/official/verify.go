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
	settled := false
	evidence := tool.Object{"schemaVersion": 3, "fixtureId": fixture.FixtureID, "mode": "verify", "pipeline": "public-agent-mock", "status": "failed", "cleanupComplete": true, "startedAt": time.Now().UTC().Format(time.RFC3339Nano)}
	defer func() {
		if activity == nil || settled {
			return
		}
		evidence["completedAt"] = time.Now().UTC().Format(time.RFC3339Nano)
		if runErr == nil {
			evidence["status"] = "passed"
		} else {
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
	evidence["materialId"] = material.ID
	materialHash := material.LegacyInventorySHA256
	if materialHash == "" {
		materialHash, err = tool.FileDigest(filepath.Join(material.Path, "material.json"))
		if err != nil {
			return err
		}
	}
	evidence["material"] = tool.Object{"path": "material/material.json", "sha256": materialHash}
	evidence["manifestSHA256"] = material.ManifestSHA256
	before, err := corpus.Inventory(material.Path, false)
	if err != nil {
		return err
	}
	if err := corpus.ValidateVerificationSource(*material, before); err != nil {
		return err
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
	evidence["id"] = activity.ID
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
	root := activity.Dir
	route := fixture.Routes["mock"]
	env := route.Env
	runtime := newDriverRuntime(root, route.Workspace, env["HOME"], env[map[string]string{"codex": "CODEX_HOME", "claude": "CLAUDE_CONFIG_DIR"}[fixture.AgentType]], filepath.Join(material.Path, "raw"), env["TMPDIR"], filepath.Dir(route.Workspace), true)
	private := filepath.Join(fixture.ResourceRoot, "official", activity.ID)
	peers := make(map[string]tool.Command, len(material.Connections))
	for _, id := range material.Connections {
		args := []string{"agent", "mock", "--fixture-dir", o.FixtureDir, "--connection", id}
		if o.Timeout > 0 {
			args = append(args, "--timeout", fmt.Sprint(o.Timeout.Seconds()))
		}
		peers[id] = tool.Command{Command: self, Args: args}
	}
	run, driverErr := runDriver(ctx, driverRunOptions{
		Activity:   activity,
		Driver:     driver,
		Env:        env,
		Workspace:  route.Workspace,
		Root:       root,
		PrivateDir: private,
		Material:   plan,
		Runtime:    runtime,
		Peers:      peers,
		ReportFile: "replay-report.json",
		Inspect: func(outcome, report tool.Object, priorErr error) error {
			var inspectErr error
			inspectErr = errors.Join(inspectErr, validateBusinessEvidence(fixture.AgentType, plan, bound, outcome, report))
			refs, refErr := mockReferences(o.FixtureDir, fixture, root, material.Connections, *material, previous)
			if refErr == nil {
				evidence["executions"], refErr = executionReferences(o.FixtureDir, material.ID, refs)
			}
			inspectErr = errors.Join(inspectErr, refErr)
			after, err := corpus.Inventory(material.Path, false)
			inspectErr = errors.Join(inspectErr, err)
			if !reflect.DeepEqual(before, after) {
				inspectErr = errors.Join(inspectErr, errors.New("material changed during verification"))
			}
			inspectErr = errors.Join(inspectErr, activity.RestoreMockRoute())

			return inspectErr
		},
	})
	runErr = driverErr
	if run == nil {
		return runErr
	}
	if run.ProcessGroupFailed {
		evidence["cleanupComplete"] = false
	}
	processResult := tool.Object{"schemaVersion": 3, "id": activity.ID, "fixtureId": fixture.FixtureID, "materialId": material.ID, "driverProcess": run.Process}
	if err := tool.WriteJSON(filepath.Join(activity.Dir, "result.json"), processResult); err != nil {
		return errors.Join(runErr, err)
	}
	evidence["completedAt"] = time.Now().UTC().Format(time.RFC3339Nano)
	if runErr == nil {
		evidence["status"] = "passed"
	} else {
		evidence["error"] = runErr.Error()
	}
	settleErr := activity.Complete(evidence)
	settled = true
	runErr = errors.Join(runErr, settleErr)
	if c.Index != "" && settleErr == nil {
		return admitVerified(material.Path, tool.Object{"output": activity.Dir}, runErr, c)
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
