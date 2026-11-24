package config

import (
	"context"
	"os/exec"
	"time"
)

func friendlyName() string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/usr/sbin/scutil", "--get", "ComputerName").Output()
	if err != nil {
		return ""
	}
	return string(out)
}
