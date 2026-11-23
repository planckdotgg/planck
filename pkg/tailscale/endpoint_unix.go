//go:build !darwin && !windows

package tailscale

const defaultTailscaledSocket = "/var/run/tailscale/tailscaled.sock"

func platformEndpoint() (endpoint, error) {
	return endpoint{network: "unix", address: defaultTailscaledSocket}, nil
}
