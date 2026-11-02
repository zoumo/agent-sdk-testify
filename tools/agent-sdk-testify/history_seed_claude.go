package main

import (
	"errors"
	"path/filepath"
	"strings"
)

func claudeHistorySeedReceipts(report object) (string, map[string]bool, []string, error) {
	session := ""
	agents := map[string]bool{}
	ids := []string{}
	for _, v := range array(report["receipts"]) {
		receipt := obj(v)
		if discovery := obj(receipt["list_subagents"]); len(discovery) > 0 {
			if session != "" && session != str(discovery["sessionId"]) {
				return "", nil, nil, errors.New("seed has ambiguous parents")
			}
			session = str(discovery["sessionId"])
			for _, id := range stringsOf(discovery["agentIds"]) {
				if !agents[id] {
					ids = append(ids, id)
				}
				agents[id] = true
			}
		}
	}
	return session, agents, ids, nil
}

func claudeHistorySeedIdentity(name, session string) (string, bool, error) {
	identity := ""
	if !strings.HasPrefix(name, "config/projects/") {
		return "", false, nil
	}
	base := filepath.Base(name)
	if base == session+".jsonl" {
		identity = session
	} else if strings.Contains(name, "/"+session+"/subagents/") {
		identity = strings.TrimSuffix(strings.TrimPrefix(base, "agent-"), ".jsonl")
	}
	return identity, true, nil
}
