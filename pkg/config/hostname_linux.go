package config

import "os"

func friendlyName() string {
	b, err := os.ReadFile("/etc/machine-info")
	if err != nil {
		return ""
	}
	return prettyHostname(b)
}
