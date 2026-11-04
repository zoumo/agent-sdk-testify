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
	planPath := r.Session.Source.Path
	if r.Session.Source.Kind == "recording" {
		planPath = filepath.Join(planPath, "plan.json")
	}
	sourcePlanHash, err := tool.FileDigest(planPath)
	if err != nil {
		return nil, err
	}
	origin := corpus.OfficialOrigin{RecordSourceSHA256: r.Session.Source.SHA256, RunID: r.ID, SessionID: r.Session.SessionID, CaseID: r.Session.Source.ID, AgentType: r.Session.AgentType, SourceSHA256: sourcePlanHash, DriverSHA256: driverHash, Connections: ids}
	if err := writeExclusive(filepath.Join(r.Dir, "origin.json"), origin); err != nil {
		return nil, err
	}
	claim := recordClaim{Origin: origin, Token: token, Receipts: map[string]string{}}
	for _, id := range ids {
		claim.Receipts[id] = filepath.Join(r.Dir, "driver", "peer-"+id+".process.json")
	}
	path := filepath.Join(r.Session.ResourceRoot, "official", r.ID, "claim.private.json")
	if err := writeExclusive(path, claim); err != nil {
		return nil, err
	}
	return map[string]string{"AGENT_SDK_TESTIFY_RECORD_CLAIM": path, "AGENT_SDK_TESTIFY_RECORD_TOKEN": token}, nil
}

func recordOrigin(sessionDir string, session *Session, connection string) (*corpus.OfficialOrigin, error) {
	path, token := os.Getenv("AGENT_SDK_TESTIFY_RECORD_CLAIM"), os.Getenv("AGENT_SDK_TESTIFY_RECORD_TOKEN")
	if path == "" && token == "" {
		return nil, nil
	}
	var claim recordClaim
	if err := tool.ReadJSON(path, &claim); err != nil {
		return nil, err
	}
	origin := claim.Origin
	if validateExecutionID(origin.RunID) != nil || path != filepath.Join(session.ResourceRoot, "official", origin.RunID, "claim.private.json") || token == "" || subtle.ConstantTimeCompare([]byte(token), []byte(claim.Token)) != 1 {
		return nil, errors.New("invalid official record launch capability")
	}
	if !slices.Contains(origin.Connections, connection) {
		return nil, errors.New("official launch does not declare connection")
	}
	var active tool.Object
	if err := tool.ReadJSON(filepath.Join(sessionDir, "official-active.json"), &active); err != nil {
		return nil, err
	}
	if active["id"] != origin.RunID || active["sessionId"] != session.SessionID || active["mode"] != "record" || active["status"] != "running" {
		return nil, errors.New("official record is not active")
	}
	var supervisor tool.Object
	if err := tool.ReadJSON(claim.Receipts[connection], &supervisor); err != nil {
		return nil, err
	}
	if supervisor["supervisorPid"] != float64(os.Getppid()) {
		return nil, errors.New("record was not started by the authorized supervisor")
	}
	if err := validateOfficialOrigin(sessionDir, session, &origin); err != nil {
		return nil, err
	}
	return &origin, nil
}

func validateOfficialOrigin(sessionDir string, session *Session, origin *corpus.OfficialOrigin) error {
	if origin == nil || validateExecutionID(origin.RunID) != nil || origin.SessionID != session.SessionID || origin.RecordSourceSHA256 != session.Source.SHA256 || origin.AgentType != session.AgentType || origin.CaseID != session.Source.ID {
		return errors.New("official capture origin differs from session")
	}
	var registered corpus.OfficialOrigin
	if err := tool.ReadJSON(filepath.Join(sessionDir, "official-records", origin.RunID, "origin.json"), &registered); err != nil {
		return err
	}
	if !reflect.DeepEqual(*origin, registered) {
		return errors.New("official capture origin differs from registered driver")
	}
	return nil
}
