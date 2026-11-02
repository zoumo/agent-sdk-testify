package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
)

func codexHistorySeedReceipts(report object) (string, map[string]bool, []string, error) {
	session := ""
	agents := map[string]bool{}
	ids := []string{}
	for _, v := range array(report["receipts"]) {
		receipt := obj(v)
		query := obj(receipt["history-query"])
		params := obj(query["params"])
		if query["method"] == "thread/list" && str(params["parentThreadId"]) != "" {
			if session != "" && session != str(params["parentThreadId"]) {
				return "", nil, nil, errors.New("seed has ambiguous parents")
			}
			session = str(params["parentThreadId"])
			for _, child := range array(obj(query["response"])["data"]) {
				if obj(child)["parentThreadId"] != session {
					return "", nil, nil, errors.New("seed child parent differs")
				}
				id := str(obj(child)["id"])
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
	var row object
	if e := json.Unmarshal(first, &row); e != nil {
		return "", false, e
	}
	if row["type"] == "session_meta" {
		identity = str(obj(row["payload"])["id"])
	}
	return identity, true, nil
}
