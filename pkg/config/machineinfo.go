package config

import "strings"

var unescape = strings.NewReplacer(`\"`, `"`, `\\`, `\`)

// prettyHostname is the PRETTY_HOSTNAME of an /etc/machine-info file: a
// shell-style assignment whose value may be single- or double-quoted.
func prettyHostname(machineInfo []byte) string {
	for _, line := range strings.Split(string(machineInfo), "\n") {
		key, val, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok || strings.TrimSpace(key) != "PRETTY_HOSTNAME" {
			continue
		}
		val = strings.TrimSpace(val)
		if n := len(val); n >= 2 && (val[0] == '"' || val[0] == '\'') && val[n-1] == val[0] {
			quote := val[0]
			val = val[1 : n-1]
			if quote == '"' {
				val = unescape.Replace(val)
			}
		}
		return strings.TrimSpace(val)
	}
	return ""
}
