package official

import (
	"errors"
	"path/filepath"
	"strings"

	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

func claudeHistorySeedReceipts(report tool.Object) (string, map[string]bool, []string, error) {
	session := ""
	agents := map[string]bool{}
	ids := []string{}
	for _, v := range tool.Array(report["receipts"]) {
		receipt := tool.Obj(v)
		if discovery := tool.Obj(receipt["list_subagents"]); len(discovery) > 0 {
			if session != "" && session != tool.Str(discovery["sessionId"]) {
				return "", nil, nil, errors.New("seed has ambiguous parents")
			}
			session = tool.Str(discovery["sessionId"])
			for _, id := range tool.Strings(discovery["agentIds"]) {
				if !agents[id] {
					ids = append(ids, id)
				}
				agents[id] = true
			}
		}
	}
	return session, agents, ids, nil
}

func claudeHistorySeedIdentity(name, session string) (string, bool) {
	identity := ""
	if !strings.HasPrefix(name, "config/projects/") {
		return "", false
	}
	base := filepath.Base(name)
	if base == session+".jsonl" {
		identity = session
	} else if strings.Contains(name, "/"+session+"/subagents/") {
		identity = strings.TrimSuffix(strings.TrimPrefix(base, "agent-"), ".jsonl")
	}
	return identity, true
}
