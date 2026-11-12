// Package fixturetest resolves persisted identities for installed CLI tests.
package fixturetest

import (
	"fmt"
	"path/filepath"

	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

// ExecutionRoot uses the fixture schedule, never directory order or current.
func ExecutionRoot(fixture, id string) (string, error) {
	var index struct {
		Executions []struct {
			ID         string `json:"id"`
			MaterialID string `json:"materialId"`
		} `json:"executions"`
	}
	if err := tool.ReadJSON(filepath.Join(fixture, "executions", "index.json"), &index); err != nil {
		return "", err
	}
	for _, row := range index.Executions {
		if row.ID == id {
			if !safeID(row.ID) || !safeID(row.MaterialID) {
				return "", fmt.Errorf("invalid execution identity: %q/%q", row.MaterialID, row.ID)
			}
			return filepath.Join(fixture, "packages", row.MaterialID, "executions", row.ID), nil
		}
	}
	return "", fmt.Errorf("execution %q absent from fixture schedule", id)
}

func safeID(id string) bool {
	return id != "" && id != "." && filepath.Base(id) == id && filepath.IsLocal(id)
}

// OfficialStderr returns diagnostics from explicit settled and staging domains.
func OfficialStderr(fixture string) ([]string, error) {
	var paths []string
	for _, pattern := range []string{"packages/*/record/*/stderr.log", "packages/*/verify/*/stderr.log", "failures/official-record/*/stderr.log", "runtime/official/*/staging/stderr.log"} {
		matches, err := filepath.Glob(filepath.Join(fixture, filepath.FromSlash(pattern)))
		if err != nil {
			return nil, err
		}
		paths = append(paths, matches...)
	}
	return paths, nil
}

// OwnOfficialEvidence selects settled evidence created by this fixture, excluding
// the original record and verify domains preserved in imported packages.
func OwnOfficialEvidence(fixture string, paths []string) ([]string, error) {
	var identity struct {
		FixtureID string `json:"fixtureId"`
	}
	if err := tool.ReadJSON(filepath.Join(fixture, "fixture.json"), &identity); err != nil {
		return nil, err
	}
	if identity.FixtureID == "" {
		return nil, fmt.Errorf("fixture has no fixtureId: %s", fixture)
	}
	var own []string
	for _, path := range paths {
		var evidence struct {
			FixtureID string `json:"fixtureId"`
		}
		if err := tool.ReadJSON(path, &evidence); err != nil {
			return nil, err
		}
		if evidence.FixtureID == "" {
			return nil, fmt.Errorf("official evidence has no fixtureId: %s", path)
		}
		if evidence.FixtureID == identity.FixtureID {
			own = append(own, path)
		}
	}
	return own, nil
}
