package protocol

import "github.com/zoumo/agent-sdk-testify/protocol/capture"

// IO supplies actual process descriptors so protocol handlers can interrupt
// blocked reads and writes without owning the caller's streams.
type IO = capture.IO
