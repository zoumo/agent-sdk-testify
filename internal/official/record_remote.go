package official

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/zoumo/agent-sdk-testify/internal/lifecycle"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

func waitRecordStart(ctx context.Context, fixtureDir string, fixture *lifecycle.Fixture, path string) (*lifecycle.Execution, error) {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		f, err := os.Open(path)
		if err == nil {
			var started tool.Object
			err = json.NewDecoder(f).Decode(&started)
			_ = f.Close()
			if err == nil {
				id := tool.Str(started["executionId"])
				if id == "" || filepath.Base(id) != id || !filepath.IsLocal(id) || started["status"] != "running" || started["fixtureId"] != fixture.FixtureID || started["command"] != "record" || started["connectionId"] != "main" {
					return nil, errors.New("owned service received an invalid execution start")
				}
				var execution lifecycle.Execution
				if err := tool.ReadJSON(filepath.Join(fixtureDir, "executions", id, "execution.json"), &execution); err != nil {
					return nil, err
				}
				if err := execution.Validate(*fixture); err != nil {
					return nil, err
				}
				return &execution, nil
			}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

func waitRecordPlan(ctx context.Context, path string) (tool.Object, error) {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var plan tool.Object
		if err := tool.ReadJSON(path, &plan); err == nil {
			return plan, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}
