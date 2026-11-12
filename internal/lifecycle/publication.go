package lifecycle

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"

	"github.com/zoumo/agent-sdk-testify/internal/history"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

// Publication is the explicit control journal for the only material move and
// atomic current replacement. Paths are relative to its identified package.
type Publication struct {
	MaterialID         string `json:"materialId"`
	ExecutionID        string `json:"executionId"`
	PreviousMaterialID string `json:"previousMaterialId,omitempty"`
	MaterialSHA256     string `json:"materialSHA256"`
	ManifestSHA256     string `json:"manifestSHA256"`
	Source             string `json:"source"`
	Staging            string `json:"staging"`
	Target             string `json:"target"`
}

// sealExecution writes the terminal domain inventory only after every declared
// connection has its final result. Earlier connection results remain immutable.
func sealExecution(run *transactionContext) error {
	index, err := readIndex(IndexPath(run.fixtureDir))
	if err != nil {
		return err
	}
	indexed, err := findIndexExecution(index, run.execution.ExecutionID)
	if err != nil {
		return err
	}
	if indexed == nil {
		return errors.New("execution absent from index")
	}
	status := indexed.Status
	if run.publicationCommitted {
		status = "passed"
	}
	if status != "passed" && status != "failed" {
		return nil
	}
	for _, connection := range indexed.Connections {
		var result tool.Object
		err := tool.ReadJSON(filepath.Join(run.dir, "receipts", connection.ID+".result.json"), &result)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if result["status"] == "running" {
			return nil
		}
		if result["schemaVersion"] != float64(3) || result["materialId"] != run.execution.Artifact.ID ||
			result["executionId"] != run.execution.ExecutionID || result["connectionId"] != connection.ID ||
			(result["status"] != "passed" && result["status"] != "failed") {
			return errors.New("connection result differs from its execution identity")
		}
	}
	if err := history.ValidateMaterialization(PackageRoot(run.fixtureDir, run.execution.Artifact.ID), run.dir); err != nil {
		return err
	}
	if run.execution.Command == "record" {
		if _, err := os.Stat(filepath.Join(run.dir, "isolation.json")); err == nil {
			if err := RebindNativePreparationExecution(run.fixtureDir, run.execution); err != nil {
				return err
			}
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	files, err := history.HashTree(run.dir)
	if err != nil {
		return err
	}
	inventory := map[string]string{}
	prefix := "executions/" + run.execution.ExecutionID + "/"
	for name, hash := range files {
		if name != "result.json" {
			inventory[prefix+name] = hash
		}
	}
	descriptor, err := tool.FileDigest(filepath.Join(run.dir, "execution.json"))
	if err != nil {
		return err
	}
	result := tool.Object{"schemaVersion": 1, "fixtureId": run.fixture.FixtureID, "materialId": run.execution.Artifact.ID, "executionId": run.execution.ExecutionID, "status": status, "connections": indexed.Connections, "execution": prefix + "execution.json", "executionSHA256": descriptor, "publicationCommitted": run.publicationCommitted, "files": inventory}
	if run.execution.Command == "record" && run.publicationCommitted || run.execution.Command == "mock" {
		result["material"] = "material/material.json"
		result["manifestSHA256"] = run.execution.Artifact.ManifestSHA256
		result["sourcePlanSHA256"] = run.execution.Artifact.SourcePlanSHA256
		result["planSHA256"] = run.execution.Artifact.PlanSHA256
		if run.execution.Artifact.LegacyInventorySHA256 != "" {
			result["legacyInventorySHA256"] = run.execution.Artifact.LegacyInventorySHA256
		} else {
			hash, err := tool.FileDigest(filepath.Join(run.execution.Artifact.Path, "material.json"))
			if err != nil {
				return err
			}
			result["materialSHA256"] = hash
		}
	}
	return writeExclusive(filepath.Join(run.dir, "result.json"), result)
}

// recoverPublication runs only under the exclusive action lock. It completes
// control bookkeeping from an already sealed execution; it never rewrites that
// execution, guesses a material, or treats an unclosed writer as recoverable.
func recoverPublication(fixtureDir string) error {
	index, err := readIndex(IndexPath(fixtureDir))
	if err != nil {
		return err
	}
	for _, execution := range index.Executions {
		journal := execution.Publishing
		if journal == nil || execution.Status == "passed" || execution.Status == "failed" {
			continue
		}
		if journal.ExecutionID != execution.ID || journal.MaterialID != execution.MaterialID ||
			journal.Source != "executions/"+execution.ID+"/capture" ||
			journal.Staging != ".material-staging" || journal.Target != "material" {
			return errors.New("publication journal identity or locations differ")
		}
		fixture, err := readFixtureUnlocked(fixtureDir)
		if err != nil {
			return err
		}
		current, err := readCurrent(fixtureDir, fixture.FixtureID)
		if err != nil {
			return err
		}
		if current.MaterialID != journal.MaterialID ||
			current.MaterialSHA256 != journal.MaterialSHA256 ||
			current.ManifestSHA256 != journal.ManifestSHA256 {
			return errors.New("uncommitted publication remains interrupted; journal retained")
		}
		for _, name := range []string{journal.Source, journal.Staging} {
			path, err := ResolvePackageReference(fixtureDir, execution.MaterialID, name)
			if err != nil {
				return err
			}
			if _, err := os.Lstat(path); !os.IsNotExist(err) {
				return errors.New("publication has conflicting capture or staging")
			}
		}
		if err := validateExecutionAggregate(fixtureDir, execution.MaterialID, execution.ID, execution.DescriptorSHA256); err != nil {
			return fmt.Errorf("publication execution is not sealed; journal retained: %w", err)
		}
		var aggregate struct {
			Status               string `json:"status"`
			PublicationCommitted bool   `json:"publicationCommitted"`
		}
		if err := tool.ReadJSON(filepath.Join(PackageExecutionRoot(fixtureDir, execution.MaterialID, execution.ID), "result.json"), &aggregate); err != nil {
			return err
		}
		if aggregate.Status != "passed" || !aggregate.PublicationCommitted {
			return errors.New("publication aggregate does not prove committed completion")
		}
		if err := updateIndex(fixtureDir, func(index *Index) error {
			e, err := findIndexExecution(index, execution.ID)
			if err != nil || e == nil {
				return errors.New("publication execution disappeared")
			}
			e.Status, e.ArtifactID, e.UpdatedAt = "passed", execution.MaterialID, now()
			return nil
		}); err != nil {
			return err
		}
		if err := RefreshRoutes(fixture); err != nil {
			return err
		}
	}
	return nil
}

func validateExecutionAggregate(fixtureDir, materialID, executionID, descriptorSHA256 string) error {
	root := PackageExecutionRoot(fixtureDir, materialID, executionID)
	var result struct {
		SchemaVersion   int               `json:"schemaVersion"`
		MaterialID      string            `json:"materialId"`
		ExecutionID     string            `json:"executionId"`
		Execution       string            `json:"execution"`
		ExecutionSHA256 string            `json:"executionSHA256"`
		Files           map[string]string `json:"files"`
	}
	if err := tool.ReadJSON(filepath.Join(root, "result.json"), &result); err != nil {
		return err
	}
	prefix := "executions/" + executionID + "/"
	if result.SchemaVersion != 1 || result.MaterialID != materialID ||
		result.ExecutionID != executionID || result.Execution != prefix+"execution.json" ||
		result.ExecutionSHA256 != descriptorSHA256 {
		return errors.New("execution aggregate identity differs from its package")
	}
	hash, err := tool.FileDigest(filepath.Join(root, "execution.json"))
	if err != nil || hash != descriptorSHA256 {
		return errors.New("execution descriptor differs from its sealed hash")
	}
	files, err := history.HashTree(root)
	if err != nil {
		return err
	}
	actual := make(map[string]string, len(files)-1)
	for name, hash := range files {
		if name != "result.json" {
			actual[prefix+name] = hash
		}
	}
	if !reflect.DeepEqual(actual, result.Files) {
		return errors.New("execution aggregate inventory differs from its domain")
	}
	return nil
}
