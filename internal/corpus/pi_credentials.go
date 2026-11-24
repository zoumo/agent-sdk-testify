package corpus

import (
	"bytes"
	"encoding/json"
	"strings"
)

type piModelAliasMessage struct {
	Role     string          `json:"role"`
	Provider json.RawMessage `json:"provider"`
}

type piModelAliasEvent struct {
	Type     string                `json:"type"`
	Command  string                `json:"command"`
	Success  bool                  `json:"success"`
	Provider json.RawMessage       `json:"provider"`
	Message  piModelAliasMessage   `json:"message"`
	Messages []piModelAliasMessage `json:"messages"`
	Data     struct {
		Model struct {
			Provider json.RawMessage `json:"provider"`
		} `json:"model"`
	} `json:"data"`
}

type piModelAliasFrame struct {
	Direction string            `json:"direction"`
	Wire      piModelAliasEvent `json:"wire"`
}

// Count only the known occurrences of a Pi model alias in typed routing
// metadata. The credential scan compares this count with the original bytes.
// Additional occurrences, including in request or response text, fail the scan.
func piModelAliasMetadata(path string, data []byte, alias string) int {
	switch {
	case path == "raw/manifest.json":
		var manifest struct {
			Native struct {
				Args []json.RawMessage `json:"args"`
			} `json:"native"`
		}
		if json.Unmarshal(data, &manifest) != nil {
			return 0
		}
		args := manifest.Native.Args
		prefix := []byte(`"` + alias + "/")
		count := 0
		for i := 1; i < len(args); i++ {
			if string(args[i-1]) == `"--model"` && bytes.HasPrefix(args[i], prefix) {
				count++
			}
		}
		return count
	case path == "raw/raw.jsonl":
		return countPiModelAliasLines(data, alias, func(frame piModelAliasFrame, literal string) int {
			if frame.Direction != "s2c" {
				return 0
			}
			return piWireModelAliasCount(frame.Wire, literal)
		})
	case strings.HasPrefix(path, "history/") && strings.HasSuffix(path, ".jsonl"):
		return countPiModelAliasLines(data, alias, piHistoryModelAliasCount)
	default:
		return 0
	}
}

func countPiModelAliasLines[T any](data []byte, alias string, count func(T, string) int) int {
	total := 0
	literal := `"` + alias + `"`
	for line := range bytes.SplitSeq(data, []byte{'\n'}) {
		if !bytes.Contains(line, []byte(alias)) {
			continue
		}
		var value T
		if json.Unmarshal(line, &value) != nil {
			return 0
		}
		total += count(value, literal)
	}
	return total
}

func piWireModelAliasCount(wire piModelAliasEvent, literal string) int {
	switch wire.Type {
	case "response":
		if wire.Command == "get_state" && wire.Success && string(wire.Data.Model.Provider) == literal {
			return 1
		}
	case "message_start", "message_update", "message_end", "turn_end":
		return piMessageModelAliasCount(wire.Message, literal)
	case "agent_end":
		count := 0
		for _, message := range wire.Messages {
			count += piMessageModelAliasCount(message, literal)
		}
		return count
	}
	return 0
}

func piHistoryModelAliasCount(entry piModelAliasEvent, literal string) int {
	switch entry.Type {
	case "model_change":
		if string(entry.Provider) == literal {
			return 1
		}
	case "message":
		return piMessageModelAliasCount(entry.Message, literal)
	}
	return 0
}

func piMessageModelAliasCount(message piModelAliasMessage, literal string) int {
	if message.Role == "assistant" && string(message.Provider) == literal {
		return 1
	}
	return 0
}
