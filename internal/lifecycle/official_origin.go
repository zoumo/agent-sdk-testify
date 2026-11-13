package lifecycle

import (
	"crypto/subtle"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"

	"github.com/zoumo/agent-sdk-testify/internal/corpus"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

type recordClaim struct {
	Origin   corpus.OfficialOrigin `json:"origin"`
	Token    string                `json:"token"`
	Receipts map[string]string     `json:"receipts"`
}

// AuthorizeRecord supplies launch-only context to the existing supervisor. The
// driver receives no capability; the child must also prove its supervisor PID.
func (r *OfficialRun) AuthorizeRecord(driverHash string, ids []string) (map[string]string, error) {
	token, err := randomID()
	if err != nil {
		return nil, err
	}
	planPath := r.Fixture.Source.Path

	sourcePlanHash, err := tool.FileDigest(planPath)
	if err != nil {
		return nil, err
	}
	origin := corpus.OfficialOrigin{RecordSourceSHA256: r.Fixture.Source.SHA256, RunID: r.ID, FixtureID: r.Fixture.FixtureID, CaseID: r.Fixture.Source.ID, AgentType: r.Fixture.AgentType, SourceSHA256: sourcePlanHash, DriverSHA256: driverHash, Connections: ids}
	if err := writeExclusive(filepath.Join(r.Dir, "origin.json"), origin); err != nil {
		return nil, err
	}
	claim := recordClaim{Origin: origin, Token: token, Receipts: map[string]string{}}
	for _, id := range ids {
		claim.Receipts[id] = filepath.Join(r.Dir, "peer-"+id+".process.json")
	}
	path := filepath.Join(r.Fixture.ResourceRoot, "official", r.ID, "claim.private.json")
	if err := writeExclusive(path, claim); err != nil {
		return nil, err
	}
	return map[string]string{"AGENT_SDK_TESTIFY_RECORD_CLAIM": path, "AGENT_SDK_TESTIFY_RECORD_TOKEN": token}, nil
}

func recordOrigin(fixtureDir string, fixture *Fixture, connection string) (*corpus.OfficialOrigin, error) {
	path, token := os.Getenv("AGENT_SDK_TESTIFY_RECORD_CLAIM"), os.Getenv("AGENT_SDK_TESTIFY_RECORD_TOKEN")
	if path == "" && token == "" {
		return nil, nil
	}
	var claim recordClaim
	if err := tool.ReadJSON(path, &claim); err != nil {
		return nil, err
	}
	origin := claim.Origin
	if validateExecutionID(origin.RunID) != nil || path != filepath.Join(fixture.ResourceRoot, "official", origin.RunID, "claim.private.json") || token == "" || subtle.ConstantTimeCompare([]byte(token), []byte(claim.Token)) != 1 {
		return nil, errors.New("invalid official record launch capability")
	}
	if !slices.Contains(origin.Connections, connection) {
		return nil, errors.New("official launch does not declare connection")
	}
	var active tool.Object
	if err := persistedSchema(filepath.Join(fixtureDir, "official.json"), "official activity"); err != nil {
		return nil, err
	}
	if err := tool.ReadJSON(filepath.Join(fixtureDir, "official.json"), &active); err != nil {
		return nil, err
	}
	if active["id"] != origin.RunID || active["fixtureId"] != fixture.FixtureID || active["mode"] != "record" || active["status"] != "running" {
		return nil, errors.New("official record is not active")
	}
	var supervisor tool.Object
	if err := tool.ReadJSON(claim.Receipts[connection], &supervisor); err != nil {
		return nil, err
	}
	if supervisor["supervisorPid"] != float64(os.Getppid()) {
		return nil, errors.New("record was not started by the authorized supervisor")
	}
	if err := validateOfficialOrigin(fixtureDir, fixture, &origin); err != nil {
		return nil, err
	}
	return &origin, nil
}

func validateOfficialOrigin(fixtureDir string, fixture *Fixture, origin *corpus.OfficialOrigin) error {
	if origin == nil || validateExecutionID(origin.RunID) != nil || origin.FixtureID != fixture.FixtureID || origin.RecordSourceSHA256 != fixture.Source.SHA256 || origin.AgentType != fixture.AgentType || origin.CaseID != fixture.Source.ID {
		return errors.New("official capture origin differs from fixture")
	}
	var registered corpus.OfficialOrigin
	if err := tool.ReadJSON(filepath.Join(ResourceRoot(fixtureDir), "official", origin.RunID, "staging", "origin.json"), &registered); err != nil {
		return err
	}
	if !reflect.DeepEqual(*origin, registered) {
		return errors.New("official capture origin differs from registered driver")
	}
	return nil
}
