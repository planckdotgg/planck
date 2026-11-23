//go:build windows

package all

import (
	_ "github.com/planckdotgg/planck/pkg/encode/amf"
	_ "github.com/planckdotgg/planck/pkg/encode/nvenc"
	_ "github.com/planckdotgg/planck/pkg/encode/vpl"
)
