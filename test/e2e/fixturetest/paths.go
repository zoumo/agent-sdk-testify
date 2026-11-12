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
