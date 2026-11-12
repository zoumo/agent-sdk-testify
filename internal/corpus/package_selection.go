package corpus

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/zoumo/agent-sdk-testify/internal/history"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

// PackageSelectionReference selects one sealed domain envelope. References are
// package-relative and preserve the identity already present in that envelope.
type PackageSelectionReference struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

// PackageSelection is the schema 2 reader contract shared with admission. The
// domain envelopes own inventories; review only selects their exact hashes.
type PackageSelection struct {
	SchemaVersion int                         `json:"schemaVersion"`
	MaterialID    string                      `json:"materialId"`
	Material      PackageSelectionReference   `json:"material"`
	Record        PackageSelectionReference   `json:"record"`
	Verification  PackageSelectionReference   `json:"verification"`
	Executions    []PackageSelectionReference `json:"executions"`
}

func packageID(id string) bool {
	return id != "" && id != "." && id != ".." && !strings.ContainsAny(id, "/\\\x00") && filepath.IsLocal(id)
}

// ValidatePackage validates imported immutable domains without registering any
// imported execution in the new fixture's scheduling index.
func ValidatePackage(root string, material Material) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		switch entry.Name() {
		case "material":
		case "review.json", "adoption.json":
			if !entry.Type().IsRegular() {
				return errors.New("package selection is not a regular file")
			}
		case "executions", "record", "verify":
			if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
				return errors.New("invalid package evidence root")
			}
			domains, err := os.ReadDir(filepath.Join(root, entry.Name()))
			if err != nil {
				return err
			}
			for _, domain := range domains {
				if !packageID(domain.Name()) || !domain.IsDir() || domain.Type()&os.ModeSymlink != 0 {
					return errors.New("invalid imported domain identity")
				}
				if err := validatePackageDomain(root, entry.Name(), domain.Name(), material); err != nil {
					return err
				}
			}
		default:
			return fmt.Errorf("unsupported package entry: %s", entry.Name())
		}
	}
	return ValidatePackageSelection(root, material)
}

func validatePackageDomain(root, kind, id string, material Material) error {
	envelope := "result.json"
	version := float64(1)
	identity := "executionId"
	if kind == "record" {
		version = 3
		identity = "id"
	}
	if kind == "verify" {
		envelope = "verification.json"
		version = 3
		identity = "id"
	}
	prefix := path.Join(kind, id)
	var object tool.Object
	if err := tool.ReadJSON(filepath.Join(root, filepath.FromSlash(prefix), envelope), &object); err != nil {
		return err
	}
	if object["schemaVersion"] != version || object[identity] != id || object["materialId"] != material.ID {
		return errors.New("imported domain version or identity differs")
	}
	files, err := history.HashTree(filepath.Join(root, filepath.FromSlash(prefix)))
	if err != nil {
		return err
	}
	delete(files, envelope)
	inventory := tool.Obj(object["files"])
	if len(files) != len(inventory) {
		return errors.New("imported domain inventory is incomplete")
	}
	for name, digest := range files {
		if inventory[path.Join(prefix, name)] != digest {
			return errors.New("imported domain inventory hash differs")
		}
	}
	if kind == "executions" {
		descriptor := prefix + "/execution.json"
		if object["execution"] != descriptor {
			return errors.New("imported execution descriptor reference differs")
		}
		if err := VerifyFile(root, descriptor, tool.Str(object["executionSHA256"])); err != nil {
			return err
		}
		var declaration tool.Object
		if err := tool.ReadJSON(filepath.Join(root, filepath.FromSlash(descriptor)), &declaration); err != nil {
			return err
		}
		if declaration["schemaVersion"] != float64(7) || declaration["executionId"] != id || tool.Obj(declaration["artifact"])["id"] != material.ID {
			return errors.New("imported execution declaration identity differs")
		}
		if err := history.ValidateMaterialization(root, filepath.Join(root, filepath.FromSlash(prefix))); err != nil {
			return err
		}
		if object["status"] == "passed" {
			if object["material"] != "material/material.json" || object["planSHA256"] != material.PlanSHA256 ||
				object["sourcePlanSHA256"] != material.SourcePlanSHA256 || object["manifestSHA256"] != material.ManifestSHA256 {
				return errors.New("imported execution core binding differs")
			}
			if err := VerifyFile(root, "material/material.json", tool.Str(object["materialSHA256"])); err != nil {
				return err
			}
		}
	}
	return nil
}

func ValidatePackageSelection(root string, material Material) error {
	var review PackageSelection
	var adoption tool.Object
	re := tool.ReadJSON(filepath.Join(root, "review.json"), &review)
	ae := tool.ReadJSON(filepath.Join(root, "adoption.json"), &adoption)
	if os.IsNotExist(re) && os.IsNotExist(ae) {
		return nil
	}
	if re != nil {
		return re
	}
	if ae != nil {
		return ae
	}
	if review.SchemaVersion != 2 || adoption["schemaVersion"] != float64(2) {
		return errors.New("unsupported package selection version")
	}
	if review.MaterialID != material.ID || adoption["materialId"] != material.ID || adoption["adoption"] != "admitted" {
		return errors.New("package selection identity differs")
	}
	hash, err := tool.FileDigest(filepath.Join(root, "review.json"))
	if err != nil {
		return err
	}
	if adoption["reviewSHA256"] != hash {
		return errors.New("adoption review hash differs")
	}
	if _, ok := adoption["files"]; ok {
		return errors.New("adoption duplicates domain inventory")
	}
	var reviewFields tool.Object
	if err := tool.ReadJSON(filepath.Join(root, "review.json"), &reviewFields); err != nil {
		return err
	}
	if _, ok := reviewFields["files"]; ok {
		return errors.New("review duplicates domain inventory")
	}
	check := func(ref PackageSelectionReference, kind string) (tool.Object, error) {
		parts := strings.Split(ref.Path, "/")
		if kind == "material" {
			if ref.Path != "material/material.json" {
				return nil, errors.New("invalid selected material reference")
			}
		} else {
			envelope := "result.json"
			if kind == "verify" {
				envelope = "verification.json"
			}
			if len(parts) != 3 || parts[0] != kind || !packageID(parts[1]) || parts[2] != envelope {
				return nil, errors.New("invalid selected domain reference")
			}
		}
		if err := VerifyFile(root, ref.Path, ref.SHA256); err != nil {
			return nil, err
		}
		var object tool.Object
		if err := tool.ReadJSON(filepath.Join(root, filepath.FromSlash(ref.Path)), &object); err != nil {
			return nil, err
		}
		if kind == "material" {
			if object["id"] != material.ID || object["formatVersion"] != float64(3) {
				return nil, errors.New("selected material identity differs")
			}
		} else if err := validatePackageDomain(root, kind, parts[1], material); err != nil {
			return nil, err
		}
		return object, nil
	}
	if _, err := check(review.Material, "material"); err != nil {
		return err
	}
	record, err := check(review.Record, "record")
	if err != nil {
		return err
	}
	verification, err := check(review.Verification, "verify")
	if err != nil {
		return err
	}
	if record["status"] != "passed" || verification["status"] != "passed" {
		return errors.New("selection refers to unsuccessful official evidence")
	}
	selected := map[string]bool{}
	for _, ref := range review.Executions {
		object, err := check(ref, "executions")
		if err != nil {
			return err
		}
		id := tool.Str(object["executionId"])
		if selected[id] {
			return errors.New("duplicate selected execution")
		}
		selected[id] = true
		if object["status"] != "passed" {
			return errors.New("selection refers to unsuccessful execution")
		}
	}
	required := map[string]bool{}
	if id := tool.Str(record["executionId"]); id != "" {
		required[id] = true
	} else {
		return errors.New("selected record omits execution")
	}
	if len(tool.Array(verification["executions"])) == 0 {
		return errors.New("selected verification omits executions")
	}
	for _, value := range tool.Array(verification["executions"]) {
		id := tool.Str(tool.Obj(value)["executionId"])
		if id == "" {
			return errors.New("selected verification omits execution identity")
		}
		required[id] = true
	}
	if len(required) < 2 || !reflect.DeepEqual(selected, required) {
		return errors.New("selected execution closure differs from official evidence")
	}
	return nil
}
