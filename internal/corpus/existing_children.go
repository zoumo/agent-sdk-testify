package corpus

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/zoumo/agent-sdk-testify/internal/history"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

func SelfSeededChildren(plan tool.Object) bool {
	return plan["driverProfile"] == "existing-children" && plan["historySeed"] == nil &&
		tool.Obj(tool.Obj(plan["scenario"])["parameters"])["selfSeed"] == true
}

func ValidateChildSetup(root string, plan tool.Object) error {
	var setup tool.Object
	if err := tool.ReadJSON(filepath.Join(root, "existing-children", "setup.json"), &setup); err != nil {
		return err
	}
	if !reflect.DeepEqual(setup, plan["existingChildrenSetup"]) {
		return errors.New("existing-child setup differs from material facts")
	}
	if _, err := ChildObservationArgs(plan); err != nil {
		return err
	}
	if _, err := time.Parse(time.RFC3339Nano, tool.Str(setup["completedAt"])); err != nil {
		return errors.New("existing-child setup completion time is absent")
	}
	parent := tool.Str(setup["sessionId"])
	children := tool.Strings(setup["agentIds"])
	if len(children) != 1 || children[0] == "" || children[0] == parent {
		return errors.New("existing-child setup requires one distinct child")
	}
	var bindings []history.Binding
	encoded, err := json.Marshal(setup["historyBindings"])
	if err != nil {
		return err
	}
	if err := json.Unmarshal(encoded, &bindings); err != nil {
		return err
	}
	primary, targets := map[string]bool{}, map[string]bool{}
	for _, binding := range bindings {
		if binding.Role != "seed" || binding.Source != "history/seed/"+binding.NativeTarget || targets[binding.NativeTarget] ||
			(binding.SessionID != parent && binding.SessionID != children[0]) ||
			(binding.SessionID == parent && binding.ParentSessionID != "") ||
			(binding.SessionID == children[0] && (binding.ParentSessionID != parent || binding.ChildSessionID != children[0])) {
			return errors.New("existing-child setup history identity or binding differs")
		}
		targets[binding.NativeTarget] = true
		if err := VerifyFile(root, binding.Source, binding.SHA256); err != nil {
			return err
		}
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(binding.Source)))
		if err != nil {
			return err
		}
		identity, err := history.Identify(binding.NativeTarget, data, binding.Project)
		if err != nil || identity.SessionID != binding.SessionID || identity.ParentSessionID != binding.ParentSessionID ||
			identity.ChildSessionID != binding.ChildSessionID || identity.Project != binding.Project {
			return errors.New("existing-child setup differs from native saved identity")
		}
		if strings.HasSuffix(binding.NativeTarget, ".jsonl") {
			if primary[binding.SessionID] {
				return errors.New("existing-child setup repeats native identity")
			}
			primary[binding.SessionID] = true
		}
	}
	if len(primary) != 2 || !primary[parent] || !primary[children[0]] {
		return errors.New("existing-child setup omits native parent or child history")
	}
	for name, key := range map[string]string{"raw/raw.jsonl": "rawSHA256", "raw/transport.json": "transportSHA256"} {
		if err := VerifyFile(root, name, tool.Str(setup[key])); err != nil {
			return err
		}
	}
	var transport tool.Object
	if err := tool.ReadJSON(filepath.Join(root, "raw", "transport.json"), &transport); err != nil {
		return err
	}
	if transport["captureStatus"] != "complete" || transport["nativeExitCode"] != float64(0) ||
		transport["nativeLaunched"] != true || transport["reaped"] != true || transport["clientEOF"] != true ||
		transport["cleanupComplete"] != true || transport["error"] != nil {
		return errors.New("existing-child setup transport did not succeed")
	}
	return nil
}

func validateChildObservation(root string, plan tool.Object) error {
	if err := ValidateChildSetup(root, plan); err != nil {
		return err
	}
	var observation tool.Object
	if err := tool.ReadJSON(filepath.Join(root, "existing-children", "observation.json"), &observation); err != nil {
		return err
	}
	setup := tool.Obj(plan["existingChildrenSetup"])
	completed, err := time.Parse(time.RFC3339Nano, tool.Str(setup["completedAt"]))
	if err != nil {
		return err
	}
	started, err := time.Parse(time.RFC3339Nano, tool.Str(observation["startedAt"]))
	if err != nil {
		return err
	}
	if observation["schemaVersion"] != float64(1) || observation["sessionId"] != setup["sessionId"] || !started.After(completed) {
		return errors.New("existing-child observation did not follow its exact setup")
	}
	return VerifyFile(root, "existing-children/setup.json", tool.Str(observation["setupSHA256"]))
}

func ChildObservationArgs(plan tool.Object) ([]string, error) {
	setup := tool.Obj(plan["existingChildrenSetup"])
	parent := tool.Str(setup["sessionId"])
	if setup["schemaVersion"] != float64(1) || setup["provider"] != plan["provider"] ||
		!regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`).MatchString(parent) {
		return nil, errors.New("existing-child observation requires its own completed setup identity")
	}
	args := slices.Clone(tool.Strings(plan["args"]))
	if plan["provider"] == "claude" {
		args = append(args, "--resume", parent)
	}
	return args, nil
}
