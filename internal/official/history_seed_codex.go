package official

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"

	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

func codexHistorySeedReceipts(report tool.Object) (string, map[string]bool, []string, error) {
	session := ""
	agents := map[string]bool{}
	ids := []string{}
	for _, v := range tool.Array(report["receipts"]) {
		receipt := tool.Obj(v)
		query := tool.Obj(receipt["history-query"])
		params := tool.Obj(query["params"])
		if query["method"] == "thread/list" && tool.Str(params["parentThreadId"]) != "" {
			if session != "" && session != tool.Str(params["parentThreadId"]) {
				return "", nil, nil, errors.New("seed has ambiguous parents")
			}
			session = tool.Str(params["parentThreadId"])
			for _, child := range tool.Array(tool.Obj(query["response"])["data"]) {
				if tool.Obj(child)["parentThreadId"] != session {
					return "", nil, nil, errors.New("seed child parent differs")
				}
				id := tool.Str(tool.Obj(child)["id"])
				if !agents[id] {
					ids = append(ids, id)
				}
				agents[id] = true
			}
		}
	}
	return session, agents, ids, nil
}

func codexHistorySeedIdentity(name string, data []byte) (string, bool, error) {
	identity := ""
	if !strings.HasPrefix(name, "config/sessions/") {
		return "", false, nil
	}
	first, _, _ := bytes.Cut(data, []byte{'\n'})
	var row tool.Object
	if e := json.Unmarshal(first, &row); e != nil {
		return "", false, e
	}
	if row["type"] == "session_meta" {
		identity = tool.Str(tool.Obj(row["payload"])["id"])
	}
	return identity, true, nil
}
