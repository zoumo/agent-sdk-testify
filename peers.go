package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The selected runs own both the commands sent to the driver and the evidence
// required afterwards. Verification never rediscovers runs from output files.
type peerRuns struct {
	mode, provider string
	runs           []peerRun
	driver         driverPeer
	remote         *mockRemoteService
}

type peerRun struct {
	name, recording, receipt string
}

func preparePeerRuns(ctx context.Context, o options, plan object, service *remoteService) (*peerRuns, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	peerName := o.Peer
	if peerName == "" {
		peerName = self
	}
	peer, err := absoluteExecutable(peerName)
	if err != nil {
		return nil, err
	}
	r := &peerRuns{mode: o.Mode, provider: str(plan["provider"])}
	root := o.Output
	phases := []string{"main"}
	if plan["driverProfile"] == "remote-close" {
		phases = append(phases, "probe")
	}
	if o.Mode == "simulate" && str(obj(obj(plan["scenario"])["parameters"])["childDetailPolicy"]) != "" {
		phases = append(phases, "suppress")
	}
	for _, phase := range phases {
		base, source := root, o.Materials
		switch phase {
		case "probe":
			base = filepath.Join(root, "remote-probe")
			source = filepath.Join(o.Materials, "remote-probe")
			if err = os.MkdirAll(base, 0700); err != nil {
				return nil, err
			}
		case "suppress":
			base = filepath.Join(root, "suppress")
			if err = preparePolicyCopy(root, base); err != nil {
				return nil, err
			}
		}
		run := peerRun{name: phase, recording: filepath.Join(base, "raw/raw.jsonl"), receipt: filepath.Join(base, "replay-peer.json")}
		var args []string
		if o.Mode == "record" {
			run.receipt = filepath.Join(base, "raw/transport.json")
			cwd := str(plan["project"])
			if plan["driverProfile"] == "existing-children" {
				cwd = str(plan["historyProject"])
			}
			args = []string{"protocol", "record", "--output", filepath.Join(base, "raw"), "--cwd", cwd, "--stdin-eof-action", str(plan["stdinEOFAction"]), "--", str(plan["cli"])}
			args = append(args, stringsOf(plan["args"])...)
			if service != nil {
				args = []string{"protocol", "record", "--output", filepath.Join(base, "raw"), "--cwd", str(plan["project"]), "--", peer, "protocol", "connect", "--url", service.endpoint, "--token-file", service.token}
			}
		} else {
			// The original native cwd and recording bytes remain source data;
			// only writable fixtures and output receipts belong to this attempt.
			run.recording = filepath.Join(source, "raw/raw.jsonl")
			if _, err = os.Stat(run.recording); err != nil {
				return nil, fmt.Errorf("%s offline recording: %w", phase, err)
			}
			if phase != "suppress" {
				if err = os.Symlink(filepath.Join(source, "raw"), filepath.Join(base, "raw")); err != nil {
					return nil, err
				}
			}
			args = []string{"protocol", "mock", "--provider", r.provider, "--scenario", run.recording, "--receipt", run.receipt}
		}
		peerPath := filepath.Join(root, "peer-"+phase+".json")
		if err = writeJSON(peerPath, command{Command: peer, Args: args}); err != nil {
			return nil, err
		}
		launcher, err := writePeerLauncher(root, phase, command{Command: self, Args: []string{"peer", "--request", peerPath}})
		if err != nil {
			return nil, err
		}
		entry := command{Command: launcher}
		switch phase {
		case "main":
			r.driver.command = entry
		case "probe":
			r.driver.Probe = &entry
		case "suppress":
			r.driver.Suppress = &entry
		}
		r.runs = append(r.runs, run)
	}
	if o.Mode == "simulate" && plan["driverProfile"] == "remote-close" {
		r.remote, err = startMockRemote(ctx, r.driver.command, *r.driver.Probe, root)
		if err != nil {
			return nil, err
		}
		r.driver.Remote = &r.remote.endpoint
	}
	return r, nil
}

func (r *peerRuns) close() error {
	if r.remote != nil {
		return r.remote.close()
	}
	return nil
}

func (r *peerRuns) finish(status string) error {
	err := r.close()
	if status == "unsupported" {
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
	var receipt object
	if err := readJSON(r.receipt, &receipt); err != nil {
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
	return nil
}

// Launchers adapt SDKs that accept only an executable path. The runner alone
// chooses the protocol command, inputs and receipt for each independent run.
func writePeerLauncher(root, phase string, peer command) (string, error) {
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	words := []string{quote(peer.Command)}
	for _, arg := range peer.Args {
		words = append(words, quote(arg))
	}
	path := filepath.Join(root, "peer-"+phase)
	return path, os.WriteFile(path, []byte("#!/bin/sh\nexec "+strings.Join(words, " ")+"\n"), 0700)
}

func preparePolicyCopy(root, target string) error {
	if err := os.Mkdir(target, 0700); err != nil {
		return err
	}
	for _, name := range []string{"raw", "config", "plan.json", "report.json"} {
		if err := os.Symlink(filepath.Join(root, name), filepath.Join(target, name)); err != nil {
			return err
		}
	}
	return nil
}
