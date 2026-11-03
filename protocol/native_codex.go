package protocol

var codexNativeArguments = nativeArgumentSpec{
	bare: map[string]bool{"app-server": true},
	values: map[string]string{
		"--listen": "stdio://",
	},
}
