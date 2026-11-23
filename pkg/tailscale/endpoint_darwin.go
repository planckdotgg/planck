package tailscale

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// The macOS GUI builds of Tailscale serve LocalAPI on a localhost TCP port
// guarded by a token, published in a "sameuserproof" file. The standalone
// build writes /Library/Tailscale/ipnport (a symlink whose target is the port)
// and sameuserproof-<port> holding the token, readable by the admin group.
// The App Store build keeps sameuserproof-<port>-<token> open inside its
// sandbox container, so the only way to find it is lsof on IPNExtension.
const (
	macsysDir              = "/Library/Tailscale"
	darwinTailscaledSocket = "/var/run/tailscaled.socket"
)

func platformEndpoint() (endpoint, error) {
	if ep, err := macsysEndpoint(macsysDir); err == nil {
		return ep, nil
	}
	if ep, err := appStoreEndpoint(); err == nil {
		return ep, nil
	}
	if _, err := os.Stat(darwinTailscaledSocket); err == nil {
		return endpoint{network: "unix", address: darwinTailscaledSocket}, nil
	}
	return endpoint{}, fmt.Errorf("%w: no LocalAPI endpoint found", ErrNotRunning)
}

func macsysEndpoint(dir string) (endpoint, error) {
	portStr, err := os.Readlink(filepath.Join(dir, "ipnport"))
	if err != nil {
		return endpoint{}, err
	}
	if _, err := strconv.ParseUint(portStr, 10, 16); err != nil {
		return endpoint{}, fmt.Errorf("tailscale: bad ipnport %q", portStr)
	}
	tok, err := os.ReadFile(filepath.Join(dir, "sameuserproof-"+portStr))
	if err != nil {
		return endpoint{}, err
	}
	token := strings.TrimSpace(string(tok))
	if token == "" {
		return endpoint{}, errors.New("tailscale: empty sameuserproof token")
	}
	return endpoint{network: "tcp", address: "127.0.0.1:" + portStr, token: token}, nil
}

func appStoreEndpoint() (endpoint, error) {
	out, err := exec.Command("lsof", "-n", "-a", "-u"+strconv.Itoa(os.Getuid()), "-c", "IPNExtension", "-F").Output()
	if err != nil {
		return endpoint{}, err
	}
	return parseAppStoreLsof(out)
}

func parseAppStoreLsof(out []byte) (endpoint, error) {
	marker := []byte(".tailscale.ipn.macos/sameuserproof-")
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		_, after, ok := bytes.Cut(sc.Bytes(), marker)
		if !ok {
			continue
		}
		portStr, token, ok := strings.Cut(string(after), "-")
		if !ok || token == "" {
			continue
		}
		if _, err := strconv.ParseUint(portStr, 10, 16); err != nil {
			continue
		}
		return endpoint{network: "tcp", address: "127.0.0.1:" + portStr, token: token}, nil
	}
	return endpoint{}, errors.New("tailscale: no App Store sameuserproof file open")
}
