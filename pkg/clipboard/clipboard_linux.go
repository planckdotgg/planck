package clipboard

import (
	"context"
	"io"
	"os"
	"os/exec"
)

func openBoard() (Board, error) {
	wayland, xsel, err := chooseTools(sessionEnv{
		wayland: os.Getenv("WAYLAND_DISPLAY"),
		display: os.Getenv("DISPLAY"),
		have:    func(tool string) bool { _, err := exec.LookPath(tool); return err == nil },
	})
	if err != nil {
		return nil, err
	}
	return &cmdBoard{wayland: wayland, xsel: xsel, run: execRun, interval: cmdPollInterval}, nil
}

// execRun runs name. Stdout is read through a pipe only when the caller
// wants output: the writing tools (wl-copy, xclip) leave a background
// process serving the selection, which would hold a pipe open and block
// the wait, so their stdout and stderr go to /dev/null.
func execRun(ctx context.Context, stdin []byte, maxOut int, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if stdin != nil {
		pr, pw, err := os.Pipe()
		if err != nil {
			return nil, err
		}
		cmd.Stdin = pr
		defer pr.Close()
		if err := cmd.Start(); err != nil {
			pw.Close()
			return nil, err
		}
		pr.Close()
		_, werr := pw.Write(stdin)
		pw.Close()
		if err := cmd.Wait(); err != nil {
			return nil, err
		}
		return nil, werr
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	data, rerr := io.ReadAll(io.LimitReader(out, int64(maxOut)+1))
	if len(data) > maxOut {
		_ = cmd.Process.Kill()
	}
	werr := cmd.Wait()
	if len(data) > maxOut {
		return data, nil
	}
	if rerr != nil {
		return nil, rerr
	}
	return data, werr
}
