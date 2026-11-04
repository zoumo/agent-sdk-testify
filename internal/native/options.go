package native

// Options contains only native preparation and owned-service inputs.
type Options struct {
	CLI, Config, Model, InitialModel string
	RuntimeEnv                       map[string]string
	ProbeProcess                     func(string, int) error `json:"-"`
	ProcessStarted                   func(int) error         `json:"-"`
}
