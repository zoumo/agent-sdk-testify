package mock

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestReplayTimeoutPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, agent              string
		explicit, scenario, want time.Duration
	}{
		{"codex default", "codex", 0, 0, 30 * time.Second},
		{"claude default", "claude", 0, 0, 10 * time.Second},
		{"codex scenario", "codex", 0, 40 * time.Millisecond, 40 * time.Millisecond},
		{"claude scenario", "claude", 0, 40 * time.Millisecond, 40 * time.Millisecond},
		{"codex explicit", "codex", time.Second, time.Millisecond, time.Second},
		{"claude explicit", "claude", time.Second, time.Millisecond, time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) { require.Equal(t, tc.want, replayTimeout(tc.agent, tc.explicit, tc.scenario)) })
	}
}
