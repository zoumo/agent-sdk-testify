package mock

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io/fs"
	"os"
	"strings"

	"github.com/zoumo/agent-sdk-testify/data"
	"github.com/zoumo/agent-sdk-testify/internal/catalog"
	protocolcmd "github.com/zoumo/agent-sdk-testify/protocol/command"
)

func mockQuery(args []string) error {
	f := flag.NewFlagSet("mock query", flag.ContinueOnError)
	list := f.Bool("list", false, "List full coverage")
	describe := f.Bool("describe", false, "Describe one case")
	provider := f.String("provider", "", "Provider filter")
	id := f.String("case", "", "Case ID")
	recording := f.String("recording", "", "Absolute external recording to describe")
	_ = f.Bool("json", false, "JSON output (schemaVersion 1)")
	if err := f.Parse(args); err != nil {
		return err
	}
	if *recording != "" {
		if !*describe || *list || *id != "" || f.NArg() != 0 {
			return errors.New("external query requires --describe --recording ABS, without --case")
		}
		return describeExternalMock(*recording, *provider)
	}
	if *list == *describe || f.NArg() != 0 || (*describe && (*provider == "" || *id == "")) || (*list && *id != "") {
		return errors.New("select --list or --describe --provider PROVIDER --case CASE")
	}
	if *provider != "" && *provider != "claude" && *provider != "codex" {
		return errors.New("unknown provider")
	}
	m, hash, err := readBuiltin()
	if err != nil {
		return err
	}
	rows := []object{}
	for _, coverage := range m.Coverage {
		if *provider != "" && coverage["provider"] != *provider {
			continue
		}
		if *id != "" && coverage["caseId"] != *id {
			continue
		}
		row := object{}
		for k, v := range coverage {
			row[k] = v
		}
		catalogPlan, err := catalog.Load(data.Scenarios(), catalog.Selection{Provider: str(row["provider"]), Case: str(row["caseId"])})
		if err != nil {
			return err
		}
		raw, err := fs.ReadFile(data.Scenarios(), str(row["storyId"])+".json")
		if err != nil {
			return err
		}
		var story object
		if err = json.Unmarshal(raw, &story); err != nil {
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
		if err = json.Unmarshal(raw, &binding); err != nil {
			return err
		}
		row["binding"] = obj(binding["cases"])[str(row["caseId"])]
		row["scenario"] = catalogPlan["scenario"]
		for i := range m.Materials {
			material := &m.Materials[i]
			if material.Provider == row["provider"] && material.CaseID == row["caseId"] {
				row["material"] = material
				row["plan"] = material.Plan
				if *describe {
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
	if *describe {
		if len(rows) != 1 {
			return errors.New("unknown case")
		}
		return writeNativeJSON(rows[0])
	}
	return writeNativeJSON(object{"schemaVersion": 1, "toolVersion": m.ToolVersion, "manifestSHA256": hash, "sourceRevision": m.SourceRevision, "scope": m.Scope, "cases": rows})
}

// Selection flags are removed before the existing direct/prepared parsers.
func mockBuiltinMode(ctx context.Context, args []string) (err error) {
	query := false
	for _, arg := range args {
		if arg == "--" {
			break
		}
		name, _, _ := strings.Cut(arg, "=")
		if name == "--list" || name == "--describe" {
			query = true
		}
	}
	if query {
		if err := mockQuery(args); err != nil {
			return protocolcmd.Fail(protocolcmd.ExitConfiguration, err)
		}
		return nil
	}
	var rest []string
	var id, provider string
	external, prepared := false, false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			rest = append(rest, args[i:]...)
			break
		}
		name, value, eq := strings.Cut(arg, "=")
		if name == "--recording" {
			external = true
		}
		if name == "--prepare" {
			prepared = true
		}
		if name == "--case" || name == "--provider" {
			if !eq {
				if i+1 == len(args) {
					return protocolcmd.Fail(protocolcmd.ExitConfiguration, errors.New("missing selector value"))
				}
				i++
				value = args[i]
			}
			if name == "--case" {
				if id != "" {
					return errors.New("duplicate case selector")
				}
				id = value
			} else {
				provider = value
				rest = append(rest, "--provider", value)
			}
		} else {
			rest = append(rest, arg)
		}
	}
	if id == "" {
		return mockMode(ctx, args)
	}
	if external || provider == "" {
		return protocolcmd.Fail(protocolcmd.ExitConfiguration, errors.New("--case requires --provider and excludes --recording"))
	}
	material, hash, err := selectBuiltin(provider, id)
	if err != nil {
		return protocolcmd.Fail(protocolcmd.ExitConfiguration, err)
	}
	root, err := extractBuiltin(material)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(root)) }()
	selection := &builtinSelection{ManifestSHA256: hash, Material: material}
	if prepared {
		return mockPreparedMode(ctx, append(rest, "--recording", root), selection)
	}
	return mockExternalSelected(ctx, append([]string{"--recording", root}, rest...), selection)
}
