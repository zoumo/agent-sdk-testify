package lifecycle

import (
	"crypto/subtle"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"

	"github.com/zoumo/agent-sdk-testify/internal/history"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

// OfficialOrigin identifies capture ownership, not a business pass. It is sealed
// into the material before publication; the later run result is separate evidence.
type OfficialOrigin struct {
	RunID              string   `json:"runId"`
	SessionID          string   `json:"sessionId"`
	CaseID             string   `json:"caseId"`
	AgentType          string   `json:"agentType"`
	RecordSourceSHA256 string   `json:"recordSourceSHA256"`
	SourceSHA256       string   `json:"sourceSHA256"`
	DriverSHA256       string   `json:"driverSHA256"`
	Connections        []string `json:"connections"`
}

type recordClaim struct {
	Origin   OfficialOrigin    `json:"origin"`
	Token    string            `json:"token"`
	Receipts map[string]string `json:"receipts"`
}

// ValidateOfficialCapture checks portable, sealed capture ownership. It does
// not read the originating session or imply that the parent's business passed.
func ValidateOfficialCapture(artifact Artifact) error {
	origin := artifact.OfficialOrigin
	if origin == nil || origin.RunID == "" || origin.SessionID == "" || len(origin.DriverSHA256) != 64 {
		return errors.New("official capture origin is incomplete")
	}
	var sealed OfficialOrigin
	if err := tool.ReadJSON(filepath.Join(artifact.Path, "official-origin.json"), &sealed); err != nil {
		return err
	}
	if !reflect.DeepEqual(sealed, *origin) || !slices.Equal(origin.Connections, artifact.Connections) {
		return errors.New("sealed official capture origin differs from material")
	}
	digest, err := tool.FileDigest(filepath.Join(artifact.Path, "source-plan.json"))
	if err != nil || digest != origin.SourceSHA256 {
		return errors.New("official capture lost its original plan")
	}
	var manifest, plan tool.Object
	if err := tool.ReadJSON(filepath.Join(artifact.Path, "manifest.json"), &manifest); err != nil {
		return err
	}
	if err := tool.ReadJSON(filepath.Join(artifact.Path, "plan.json"), &plan); err != nil {
		return err
	}
	if manifest["sourcePlanSHA256"] != digest || plan["sourcePlanSHA256"] != digest || plan["caseId"] != origin.CaseID || plan["provider"] != origin.AgentType {
		return errors.New("official derived plan lost its source binding")
	}
	executionID := ""
	for _, id := range origin.Connections {
		if validateExecutionID(id) != nil {
			return errors.New("invalid official connection identity")
		}
		var proof struct {
			ExecutionID   string         `json:"executionId"`
			Origin        OfficialOrigin `json:"origin"`
			ConnectionID  string         `json:"connectionId"`
			ProtocolPID   int            `json:"protocolPid"`
			SupervisorPID int            `json:"supervisorPid"`
			ToolSHA256    string         `json:"toolSHA256"`
		}
		if err := tool.ReadJSON(filepath.Join(artifact.Path, "origin", id+".json"), &proof); err != nil {
			return err
		}
		if proof.ExecutionID == "" || (executionID != "" && proof.ExecutionID != executionID) {
			return errors.New("official launch proofs disagree on execution")
		}
		executionID = proof.ExecutionID
		if !reflect.DeepEqual(proof.Origin, *origin) || proof.ConnectionID != id || proof.ProtocolPID <= 0 || proof.SupervisorPID <= 0 || len(proof.ToolSHA256) != 64 {
			return errors.New("official connection launch proof is incomplete")
		}
	}
	return nil
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
	origin := OfficialOrigin{RecordSourceSHA256: r.Session.Source.SHA256, RunID: r.ID, SessionID: r.Session.SessionID, CaseID: r.Session.Source.ID, AgentType: r.Session.AgentType, SourceSHA256: sourcePlanHash, DriverSHA256: driverHash, Connections: ids}
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

func recordOrigin(sessionDir string, session *Session, connection string) (*OfficialOrigin, error) {
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

func validateOfficialOrigin(sessionDir string, session *Session, origin *OfficialOrigin) error {
	if origin == nil || validateExecutionID(origin.RunID) != nil || origin.SessionID != session.SessionID || origin.RecordSourceSHA256 != session.Source.SHA256 || origin.AgentType != session.AgentType || origin.CaseID != session.Source.ID {
		return errors.New("official capture origin differs from session")
	}
	var registered OfficialOrigin
	if err := tool.ReadJSON(filepath.Join(sessionDir, "official-records", origin.RunID, "origin.json"), &registered); err != nil {
		return err
	}
	if !reflect.DeepEqual(*origin, registered) {
		return errors.New("official capture origin differs from registered driver")
	}
	return nil
}

// ValidateOfficialRecord is the downstream qualification seam. Capture ownership
// alone is insufficient: verify/admission must require the exact successful run,
// business report, supervised connections and unchanged material hashes.
func ValidateOfficialRecord(sessionDir string, session *Session, artifact Artifact) error {
	if err := validateOfficialOrigin(sessionDir, session, artifact.OfficialOrigin); err != nil {
		return err
	}
	root := filepath.Join(sessionDir, "official-records", artifact.OfficialOrigin.RunID)
	var result tool.Object
	if err := tool.ReadJSON(filepath.Join(root, "result.json"), &result); err != nil {
		return err
	}
	process := tool.Obj(result["driverProcess"])
	if result["status"] != "passed" || result["sessionId"] != session.SessionID || result["cleanupComplete"] != true || process["reaped"] != true || process["exitCode"] != float64(0) || tool.Obj(result["business"])["status"] != "passed" {
		return errors.New("official capture has no successful settled orchestration")
	}
	files, err := history.HashTree(filepath.Join(root, "driver"))
	if err != nil {
		return err
	}
	var expected map[string]string
	if err := tool.ReadJSON(filepath.Join(root, "driver-files.json"), &expected); err != nil {
		return err
	}
	if !reflect.DeepEqual(files, expected) {
		return errors.New("official driver evidence changed")
	}
	if tool.Obj(result["driver"])["sha256"] != artifact.OfficialOrigin.DriverSHA256 {
		return errors.New("official driver identity differs from capture")
	}
	connections := tool.Array(result["connections"])
	if len(connections) != len(artifact.Connections) {
		return errors.New("official result omits connections")
	}
	for _, id := range artifact.Connections {
		found := false
		for _, item := range connections {
			ref := tool.Obj(item)
			material := tool.Obj(ref["material"])
			if ref["connectionId"] == id && material["id"] == artifact.ID && material["manifestSHA256"] == artifact.ManifestSHA256 && material["recordingSHA256"] == artifact.RecordingSHA256 && material["planSHA256"] == artifact.PlanSHA256 && tool.Obj(ref["finished"])["status"] == "passed" {
				found = true
			}
		}
		if !found {
			return errors.New("official result does not bind exact capture")
		}
	}
	return nil
}
