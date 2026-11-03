// Package exitcode defines the process result contract shared by command
// handlers without depending on a CLI framework.
package exitcode

import "errors"

const (
	OK            = 0
	Failure       = 1
	Configuration = 2
	Canceled      = 3
	Disconnect    = 4
	Unimplemented = 5
)

type Error struct {
	Code int
	Err  error
}

func (e *Error) Error() string { return e.Err.Error() }
func (e *Error) Unwrap() error { return e.Err }

func Fail(code int, err error) error { return &Error{Code: code, Err: err} }

func Code(err error) int {
	if err == nil {
		return OK
	}
	var classified *Error
	if errors.As(err, &classified) {
		return classified.Code
	}
	return Failure
}
