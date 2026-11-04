//go:build darwin || linux

package capture

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNativeWaitCanJoinAtCleanupDeadline(t *testing.T) {
	abandon := make(chan struct{})
	result := make(chan nativeExit, 1)
	released := make(chan struct{})
	// A still-running child produces no exit. Close abandon after polling starts,
	// so the test covers the same cleanup deadline without launching a process.
	polled := make(chan struct{}, 1)
	go func() {
		result <- waitNativeExit(func() (nativeExit, bool) {
			select {
			case polled <- struct{}{}:
			default:
			}
			return nativeExit{}, false
		}, func() { close(released) }, abandon)
	}()
	select {
	case <-polled:
	case <-time.After(time.Second):
		close(abandon)
		t.Fatal("native waiter did not start polling")
	}
	close(abandon)
	select {
	case exit := <-result:
		assert.False(t, exit.reaped)
		assert.Nil(t, exit.code)
		require.ErrorContains(t, exit.err, "cleanup deadline")
		select {
		case <-released:
		default:
			t.Fatal("native handle was not released")
		}
	case <-time.After(time.Second):
		t.Fatal("native waiter did not join after cleanup deadline")
	}
}
