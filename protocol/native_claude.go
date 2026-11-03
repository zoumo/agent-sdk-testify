package protocol

var claudeNativeArguments = nativeArgumentSpec{
	flags: map[string]bool{
		"--verbose":                  true,
		"--include-partial-messages": true,
	},
	values: map[string]string{
		"--output-format":          "stream-json",
		"--input-format":           "stream-json",
		"--permission-prompt-tool": "stdio",
	},
	equalsValues: map[string]bool{
		"--model":  true,
		"--resume": true,
	},
}
