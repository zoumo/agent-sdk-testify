package application

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/zoumo/agent-sdk-testify/internal/corpus"
	"github.com/zoumo/agent-sdk-testify/internal/lifecycle"
	"github.com/zoumo/agent-sdk-testify/internal/mock"
	"github.com/zoumo/agent-sdk-testify/internal/protocol"
	"github.com/zoumo/agent-sdk-testify/internal/protocol/capture"
	"github.com/zoumo/agent-sdk-testify/internal/record"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

func openPreparedSession(sessionDir string) (*lifecycle.Session, string, error) {
	dir, err := lifecycle.CanonicalSessionDir(sessionDir)
	if err != nil {
		return nil, "", err
	}
	session, err := lifecycle.OpenSession(dir)
	if err != nil {
		return nil, "", err
	}
	return session, dir, nil
}

func prepareRecord(o RecordOptions) (record.Prepared, error) {
	if o.Connection == "" {
		o.Connection = "main"
	}
	var executable string
	var remote *capture.ConnectOptions
	var err error
	if o.URL != "" || o.TokenFile != "" {
		if o.URL == "" || o.TokenFile == "" || o.Command != "" || len(o.NativeArgs) != 0 {
			return record.Prepared{}, errors.New("remote record requires --url and --token-file, excluding local command and arguments")
		}
		remote = &capture.ConnectOptions{
			URL:            o.URL,
			TokenFile:      o.TokenFile,
			ConnectTimeout: o.ConnectTimeout,
		}
		if err := remote.Validate(); err != nil {
			return record.Prepared{}, err
		}
	} else {
		executable, err = tool.ValidateCommand(o.Command)
		if err != nil {
			return record.Prepared{}, err
		}
	}
	session, sessionDir, err := openPreparedSession(o.SessionDir)
	if err != nil {
		return record.Prepared{}, err
	}
	if remote != nil {
		if err := validateTokenLocation(sessionDir, remote.TokenFile); err != nil {
			return record.Prepared{}, err
		}
	} else if err := protocol.ValidateNativeArgs(session.AgentType, o.NativeArgs); err != nil {
		return record.Prepared{}, err
	}
	plan, err := lifecycle.LoadPlan(session)
	if err != nil {
		return record.Prepared{}, err
	}
	if !slices.Contains(lifecycle.ConnectionIDs(session.Source, plan), o.Connection) {
		return record.Prepared{}, fmt.Errorf("undeclared record connection %s", o.Connection)
	}
	args := append([]string{"--session-dir", sessionDir, "--cmd", executable, "--"}, o.NativeArgs...)
	target := "local"
	if remote != nil {
		// Connection location and authentication paths are invocation-only data.
		args = []string{"--session-dir", sessionDir, "--url", "<private>", "--token-file", "<private>"}
		token, err := os.ReadFile(remote.TokenFile)
		if err != nil {
			return record.Prepared{}, err
		}
		target = tool.DigestBytes(append(append([]byte(remote.URL), 0), token...))
	}
	return record.Prepared{
		SessionDir: sessionDir,
		Connection: o.Connection,
		Args:       args,
		NativeArgs: slices.Clone(o.NativeArgs),
		Command:    executable,
		Remote:     remote,
		Target:     target,
		Timeout:    o.Timeout,
		ResultFD:   o.ResultFD,
	}, nil
}

func prepareMock(o MockOptions) (mock.Prepared, error) {
	if o.Connection == "" {
		o.Connection = "main"
	}
	session, sessionDir, err := openPreparedSession(o.SessionDir)
	if err != nil {
		return mock.Prepared{}, err
	}
	if err := protocol.ValidateNativeArgs(session.AgentType, o.NativeArgs); err != nil {
		return mock.Prepared{}, err
	}
	material, err := currentMaterial(sessionDir, session)
	if err != nil {
		return mock.Prepared{}, err
	}
	if !slices.Contains(material.Connections, o.Connection) {
		return mock.Prepared{}, fmt.Errorf("undeclared mock connection %s", o.Connection)
	}
	if err := mock.ValidateMaterial(*material, session.AgentType); err != nil {
		return mock.Prepared{}, err
	}
	return mock.Prepared{
		SessionDir: sessionDir,
		Connection: o.Connection,
		Agent:      session.AgentType,
		Artifact:   *material,
		Timeout:    o.Timeout,
		ResultFD:   o.ResultFD,
		ProgressFD: o.ProgressFD,
		NativeArgs: slices.Clone(o.NativeArgs),
	}, nil
}

func currentMaterial(sessionDir string, session *lifecycle.Session) (*corpus.Material, error) {
	material, present, err := lifecycle.CurrentArtifact(sessionDir, session)
	if err != nil {
		return nil, err
	}
	if !present {
		return nil, errors.New("session has no current recording")
	}
	return material, nil
}

func validateTokenLocation(sessionDir, tokenFile string) error {
	tokenPath, err := filepath.EvalSymlinks(tokenFile)
	if err != nil {
		return errors.New("private token file unavailable")
	}
	if relative, err := filepath.Rel(sessionDir, tokenPath); err == nil && filepath.IsLocal(relative) {
		runtimeRelative, err := filepath.Rel(filepath.Join(sessionDir, "runtime"), tokenPath)
		if err != nil || !filepath.IsLocal(runtimeRelative) {
			return errors.New("token file must be outside session evidence (use runtime or an external file)")
		}
	}
	return nil
}
