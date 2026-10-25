//go:build !windows

package ffmpeg

import "os"

func bindToHostLifetime(*os.Process) error { return nil }
