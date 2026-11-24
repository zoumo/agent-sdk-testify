package replay

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/zoumo/agent-sdk-testify/internal/protocol/rawjson"
)

// Pi commands have optional string IDs. A response echoes both the ID (if
// present) and command type. Extension UI requests use a separate ID space.
type piAdapter struct {
	requests
	commands     map[id]string
	anonymous    map[string]int
	timedDialogs map[id]bool
}

func newPiAdapter() *piAdapter {
	return &piAdapter{requests: newRequests(), commands: map[id]string{}, anonymous: map[string]int{}, timedDialogs: map[id]bool{}}
}

func piID(value rawjson.Value) (id, error) {
	if value.Kind() != rawjson.String {
		return id{}, errors.New("pi RPC requires a string ID")
	}
	return identity(value, true)
}

func (a *piAdapter) client(expected, actual rawjson.Value) (bool, error) {
	kind := stringMember(expected, "type")
	if kind == "" {
		return false, errors.New("pi RPC command requires a type")
	}
	recorded, identified := expected.Member("id")
	if kind == "extension_ui_response" {
		key, err := piID(recorded)
		if err != nil {
			return false, err
		}
		if err := a.answer(key); err != nil {
			return false, err
		}
		return true, match(expected, actual)
	}
	if !identified {
		a.anonymous[kind]++
		return false, match(expected, actual)
	}
	key, err := piID(recorded)
	if err != nil {
		return false, err
	}
	actualID := member(actual, "id")
	live, err := piID(actualID)
	if err != nil {
		return false, err
	}
	if err := a.bind(key, live, actualID); err != nil {
		return false, err
	}
	a.commands[key] = kind
	matched, err := actual.Replace([]string{"id"}, recorded)
	if err != nil {
		return false, err
	}
	return false, match(expected, matched)
}

func (a *piAdapter) server(expected rawjson.Value) (rawjson.Value, error) {
	switch stringMember(expected, "type") {
	case "bash_execution_update":
		recorded, identified := expected.Member("id")
		if !identified {
			if a.anonymous["bash"] == 0 {
				return rawjson.Value{}, errors.New("pi bash event without pending command")
			}
			return expected, nil
		}
		key, err := piID(recorded)
		if err != nil {
			return rawjson.Value{}, err
		}
		if !a.active[key] || a.commands[key] != "bash" {
			return rawjson.Value{}, errors.New("pi bash event without matching command")
		}
		return expected.Replace([]string{"id"}, a.bindings[key])
	case "response":
		cmd := stringMember(expected, "command")
		if cmd == "" || member(expected, "success").Kind() != rawjson.Boolean {
			return rawjson.Value{}, errors.New("invalid pi RPC response")
		}
		recorded, identified := expected.Member("id")
		if !identified {
			if a.anonymous[cmd] == 0 {
				return rawjson.Value{}, errors.New("pi response without matching anonymous command")
			}
			a.anonymous[cmd]--
			return expected, nil
		}
		key, err := piID(recorded)
		if err != nil {
			return rawjson.Value{}, err
		}
		live, err := a.response(key)
		if err != nil {
			return rawjson.Value{}, err
		}
		if a.commands[key] != cmd {
			return rawjson.Value{}, errors.New("pi response command mismatch")
		}
		return expected.Replace([]string{"id"}, live)
	case "extension_ui_request":
		key, err := piID(member(expected, "id"))
		if err != nil {
			return rawjson.Value{}, err
		}
		switch stringMember(expected, "method") {
		case "":
			return rawjson.Value{}, errors.New("pi extension UI request requires a method")
		case "input", "confirm", "select", "editor":
			if err := a.reverse(key); err != nil {
				return rawjson.Value{}, err
			}
			if timeout := member(expected, "timeout"); timeout.Kind() == rawjson.Number {
				ms, err := strconv.ParseFloat(string(timeout.Raw()), 64)
				if err == nil && ms > 0 {
					a.timedDialogs[key] = true
				}
			}
		}
	}
	return expected, nil
}

func (a *piAdapter) complete() error {
	for cmd, n := range a.anonymous {
		if n != 0 {
			return fmt.Errorf("unfinished anonymous Pi command %q", cmd)
		}
	}
	// Pi resolves a timed dialog locally and emits no resolution frame.
	for key := range a.timedDialogs {
		delete(a.pending, key)
	}
	return a.requests.complete()
}
