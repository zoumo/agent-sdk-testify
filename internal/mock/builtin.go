package mock

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"maps"
	"os"

	"github.com/zoumo/agent-sdk-testify/data"
	"github.com/zoumo/agent-sdk-testify/internal/catalog"
	"github.com/zoumo/agent-sdk-testify/internal/exitcode"
)

func mockQuery(o Options) error {
	if o.Provider != "" && o.Agent != "" && o.Provider != o.Agent {
		return errors.New("--provider and --agent differ")
	}
	if o.Provider == "" {
		o.Provider = o.Agent
	}
	if o.Recording != "" {
		if !o.Describe || o.List || o.Case != "" || len(o.NativeArgs) != 0 {
			return errors.New("external query requires --describe --recording ABS, without --case")
		}
		return describeExternalMock(o.Recording, o.Provider)
	}
	if o.List == o.Describe || len(o.NativeArgs) != 0 || (o.Describe && (o.Provider == "" || o.Case == "")) || (o.List && o.Case != "") {
		return errors.New("select --list or --describe --agent AGENT --case CASE")
	}
	if o.Provider != "" && o.Provider != "claude" && o.Provider != "codex" {
		return errors.New("unknown provider")
	}
	m, hash, err := readBuiltin()
	if err != nil {
		return err
	}
	rows := []object{}
	for _, coverage := range m.Coverage {
		if o.Provider != "" && coverage["provider"] != o.Provider {
			continue
		}
		if o.Case != "" && coverage["caseId"] != o.Case {
			continue
		}
		row := object{}
		maps.Copy(row, coverage)
		catalogPlan, err := catalog.Load(data.Scenarios(), catalog.Selection{Provider: str(row["provider"]), Case: str(row["caseId"])})
		if err != nil {
			return err
		}
		raw, err := fs.ReadFile(data.Scenarios(), str(row["storyId"])+".json")
		if err != nil {
			return err
		}
		var story object
		if err := json.Unmarshal(raw, &story); err != nil {
			return err
		}
		for _, c := range array(story["cases"]) {
			if obj(c)["id"] == row["caseId"] {
				row["contract"] = c
			}
		}
		raw, err = fs.ReadFile(data.Scenarios(), "bindings/"+str(row["provider"])+".json")
		if err != nil {
			return err
		}
		var binding object
		if err := json.Unmarshal(raw, &binding); err != nil {
			return err
		}
		row["binding"] = obj(binding["cases"])[str(row["caseId"])]
		row["scenario"] = catalogPlan["scenario"]
		for i := range m.Materials {
			material := &m.Materials[i]
			if material.Provider == row["provider"] && material.CaseID == row["caseId"] {
				row["material"] = material
				row["plan"] = material.Plan
				if o.Describe {
					inputs, e := builtinAssertionInputs(material, hash)
					if e != nil {
						return e
					}
					row["assertionInputs"] = inputs
				}
			}
		}
		row["schemaVersion"] = 1
		row["manifestSHA256"] = hash
		row["toolVersion"] = m.ToolVersion
		rows = append(rows, row)
	}
	if o.Describe {
		if len(rows) != 1 {
			return errors.New("unknown case")
		}
		return writeNativeJSON(rows[0])
	}
	return writeNativeJSON(object{"schemaVersion": 1, "toolVersion": m.ToolVersion, "manifestSHA256": hash, "sourceRevision": m.SourceRevision, "scope": m.Scope, "cases": rows})
}

func mockBuiltinMode(ctx context.Context, o Options) (err error) {
	if o.Provider == "" {
		o.Provider = o.Agent
	}
	if o.Recording != "" || o.Provider == "" {
		return exitcode.Fail(exitcode.Configuration, errors.New("--case requires --agent or --provider and excludes --recording"))
	}
	material, hash, err := selectBuiltin(o.Provider, o.Case)
	if err != nil {
		return exitcode.Fail(exitcode.Configuration, err)
	}
	root, err := extractBuiltin(material)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(root)) }()
	selection := &builtinSelection{ManifestSHA256: hash, Material: material}
	o.Recording = root
	if o.Prepare {
		return mockPreparedMode(ctx, o, selection)
	}
	return mockExternalSelected(ctx, o, selection)
}
