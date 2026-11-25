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

type piModelAliasEntry struct {
	Type     string              `json:"type"`
	Provider json.RawMessage     `json:"provider"`
	Message  piModelAliasMessage `json:"message"`
}

type piModelAliasTreeNode struct {
	Entry    piModelAliasEntry      `json:"entry"`
	Children []piModelAliasTreeNode `json:"children"`
}

type piModelAliasEvent struct {
	Type     string                `json:"type"`
	Command  string                `json:"command"`
	Success  bool                  `json:"success"`
	Provider json.RawMessage       `json:"provider"`
	ModelID  string                `json:"modelId"`
	Message  piModelAliasMessage   `json:"message"`
	Messages []piModelAliasMessage `json:"messages"`
	Data     struct {
		ID       string          `json:"id"`
		Provider json.RawMessage `json:"provider"`
		Model    struct {
			Provider json.RawMessage `json:"provider"`
		} `json:"model"`
		Entries  []piModelAliasEntry    `json:"entries"`
		Messages []piModelAliasMessage  `json:"messages"`
		Tree     []piModelAliasTreeNode `json:"tree"`
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
			switch frame.Direction {
			case "c2s":
				if frame.Wire.Type == "set_model" && frame.Wire.ModelID != "" && string(frame.Wire.Provider) == literal {
					return 1
				}
			case "s2c":
				return piWireModelAliasCount(frame.Wire, literal)
			}
			return 0
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
		if !wire.Success {
			return 0
		}
		switch wire.Command {
		case "get_state":
			if string(wire.Data.Model.Provider) == literal {
				return 1
			}
		case "set_model":
			if wire.Data.ID != "" && string(wire.Data.Provider) == literal {
				return 1
			}
		case "get_entries":
			count := 0
			for _, entry := range wire.Data.Entries {
				count += piHistoryModelAliasCount(entry, literal)
			}
			return count
		case "get_messages":
			return piMessagesModelAliasCount(wire.Data.Messages, literal)
		case "get_tree":
			return piTreeModelAliasCount(wire.Data.Tree, literal)
		}
	case "message_start", "message_update", "message_end", "turn_end":
		return piMessageModelAliasCount(wire.Message, literal)
	case "agent_end":
		return piMessagesModelAliasCount(wire.Messages, literal)
	}
	return 0
}

func piHistoryModelAliasCount(entry piModelAliasEntry, literal string) int {
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

func piTreeModelAliasCount(nodes []piModelAliasTreeNode, literal string) int {
	count := 0
	for _, node := range nodes {
		count += piHistoryModelAliasCount(node.Entry, literal)
		count += piTreeModelAliasCount(node.Children, literal)
	}
	return count
}

func piMessagesModelAliasCount(messages []piModelAliasMessage, literal string) int {
	count := 0
	for _, message := range messages {
		count += piMessageModelAliasCount(message, literal)
	}
	return count
}

func piMessageModelAliasCount(message piModelAliasMessage, literal string) int {
	if message.Role == "assistant" && string(message.Provider) == literal {
		return 1
	}
	return 0
}
