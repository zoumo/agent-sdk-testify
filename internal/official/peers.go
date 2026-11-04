package official

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

// The selected runs own both the commands sent to the driver and the evidence
// required afterwards. Verification never rediscovers runs from output files.
type peerRuns struct {
	mode, provider string
	runs           []peerRun
	driver         DriverPeer
}

type peerRun struct {
	name, recording, receipt string
}

func preparePeerRuns(o Options, plan tool.Object, service *remoteService) (*peerRuns, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	peerName := o.Peer
	if peerName == "" {
		peerName = self
	}
	peer, err := tool.AbsoluteExecutable(peerName)
	if err != nil {
		return nil, err
	}
	r := &peerRuns{mode: o.Mode, provider: tool.Str(plan["provider"])}
	root := o.Output
	phases := []string{"main"}
	if plan["driverProfile"] == "remote-close" {
		phases = append(phases, "probe")
	}
	for _, phase := range phases {
		base, source := root, o.Materials
		switch phase {
		case "probe":
			base = filepath.Join(root, "remote-probe")
			source = filepath.Join(o.Materials, "remote-probe")
			if err := os.MkdirAll(base, 0o700); err != nil {
				return nil, err
			}
		}
		run := peerRun{name: phase, recording: filepath.Join(base, "raw", "raw.jsonl"), receipt: filepath.Join(base, "replay-peer.json")}
		var args []string
		if o.Mode == "record" {
			run.receipt = filepath.Join(base, "raw", "transport.json")
			cwd := tool.Str(plan["project"])
			if plan["driverProfile"] == "existing-children" {
				cwd = tool.Str(plan["historyProject"])
			}
			args = []string{"--internal-protocol", "record", "--output", filepath.Join(base, "raw"), "--cwd", cwd, "--stdin-eof-action", tool.Str(plan["stdinEOFAction"]), "--", tool.Str(plan["cli"])}
			args = append(args, tool.Strings(plan["args"])...)
			if service != nil {
				args = []string{"--internal-protocol", "record", "--output", filepath.Join(base, "raw"), "--cwd", tool.Str(plan["project"]), "--", peer, "--internal-protocol", "connect", "--url", service.endpoint, "--token-file", service.token}
			}
		} else {
			// The original native cwd and recording bytes remain source data;
			// only writable fixtures and output receipts belong to this attempt.
			run.recording = filepath.Join(source, "raw", "raw.jsonl")
			if _, err = os.Stat(run.recording); err != nil {
				return nil, fmt.Errorf("%s offline recording: %w", phase, err)
			}
			if err := os.Symlink(filepath.Join(source, "raw"), filepath.Join(base, "raw")); err != nil {
				return nil, err
			}
			args = []string{"--internal-protocol", "mock", "--provider", r.provider, "--scenario", run.recording, "--receipt", run.receipt}
		}
		peerPath := filepath.Join(root, "peer-"+phase+".json")
		if err := tool.WriteJSON(peerPath, tool.Command{Command: peer, Args: args}); err != nil {
			return nil, err
		}
		launcherArgs := []string{"--internal-peer", "--request", peerPath}
		if o.Mode == "verify" {
			launcherArgs = append(launcherArgs, "--lifecycle", filepath.Join(root, "peer-"+phase+"-lifecycle.json"))
		}
		launcher, err := WritePeerLauncher(root, phase, tool.Command{Command: self, Args: launcherArgs})
		if err != nil {
			return nil, err
		}
		entry := tool.Command{Command: launcher}
		switch phase {
		case "main":
			r.driver.Command = entry
		case "probe":
			r.driver.Probe = &entry
		}
		r.runs = append(r.runs, run)
	}
	return r, nil
}

func (r *peerRuns) finish(status string) error {
	var err error
	if status == "unsupported" && r.mode != "verify" {
		return err
	}
	for _, run := range r.runs {
		if e := run.verify(r.mode, r.provider); e != nil {
			err = errors.Join(err, fmt.Errorf("%s peer: %w", run.name, e))
		}
	}
	return err
}

func (r peerRun) verify(mode, provider string) error {
	var receipt tool.Object
	if err := tool.ReadJSON(r.receipt, &receipt); err != nil {
		return err
	}
	if mode == "record" {
		if receipt["captureStatus"] != "complete" || receipt["cleanupComplete"] != true {
			return errors.New("recording did not complete and clean up")
		}
		return nil
	}
	b, err := os.ReadFile(r.recording)
	if err != nil {
		return err
	}
	h := sha256.Sum256(b)
	if receipt["provider"] != provider || receipt["fullConsumption"] != true || receipt["clientEOF"] != true || receipt["nativeLaunched"] != false || receipt["recordingSHA256"] != hex.EncodeToString(h[:]) {
		return errors.New("mock receipt does not prove full consumption of the selected recording")
	}
	if mode == "verify" {
		if receipt["error"] != nil || receipt["frames"] != float64(len(bytes.Split(bytes.TrimSuffix(b, []byte("\n")), []byte("\n")))) {
			return errors.New("mock receipt has errors or wrong frame count")
		}
		root := filepath.Dir(r.receipt)
		if r.name == "probe" {
			root = filepath.Dir(root)
		}
		var lifecycle tool.Object
		if err := tool.ReadJSON(filepath.Join(root, "peer-"+r.name+"-lifecycle.json"), &lifecycle); err != nil {
			return err
		}
		if lifecycle["exitCode"] != float64(0) || lifecycle["reaped"] != true || lifecycle["cleanupComplete"] != true || lifecycle["error"] != nil {
			return errors.New("protocol child did not exit and clean up successfully")
		}
	}
	return nil
}

// Launchers adapt SDKs that accept only an executable path. The runner alone
// chooses the protocol command, inputs and receipt for each independent run.
func WritePeerLauncher(root, phase string, peer tool.Command) (string, error) {
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	words := []string{quote(peer.Command)}
	for _, arg := range peer.Args {
		words = append(words, quote(arg))
	}
	path := filepath.Join(root, "peer-"+phase)
	return path, os.WriteFile(path, []byte("#!/bin/sh\nexec "+strings.Join(words, " ")+"\n"), 0o700)
}
