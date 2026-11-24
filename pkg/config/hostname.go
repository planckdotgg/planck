package config

import (
	"os"
	"strings"
)

// Hostname is this machine's name as its owner knows it: the Computer
// Name of macOS Sharing settings, the computer name on Windows (os.Hostname
// reads GetComputerNameExW(ComputerNamePhysicalDnsHostname) there), and
// the host name elsewhere.
func Hostname() string {
	if n := strings.TrimSpace(friendlyName()); n != "" {
		return n
	}
	n, _ := os.Hostname()
	return strings.TrimSuffix(strings.TrimSpace(n), ".local")
}
