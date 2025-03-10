package conda

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/robocorp/rcc/common"
	"github.com/robocorp/rcc/settings"
)

const (
	Newline        = "\n"
	binSuffix      = "/bin"
	activateScript = `#!/bin/bash

export MAMBA_ROOT_PREFIX={{.Robocorphome}}
eval "$('{{.Micromamba}}' shell activate -s bash -p {{.Live}})"
"{{.Rcc}}" internal env -l after
`
	commandSuffix = ".sh"
)

var (
	FileExtensions = []string{""}
	Shell          = []string{"bash", "--noprofile", "--norc", "-i"}
)

func MicromambaLink() string {
	return settings.Global.DownloadsLink("micromamba/v0.16.0/linux64/micromamba")
}

func CondaEnvironment() []string {
	env := os.Environ()
	env = append(env, fmt.Sprintf("MAMBA_ROOT_PREFIX=%s", common.RobocorpHome()))
	tempFolder := common.RobocorpTemp()
	env = append(env, fmt.Sprintf("TEMP=%s", tempFolder))
	env = append(env, fmt.Sprintf("TMP=%s", tempFolder))
	return env
}

func BinMicromamba() string {
	return common.ExpandPath(filepath.Join(common.BinLocation(), "micromamba"))
}

func CondaPaths(prefix string) []string {
	return []string{common.ExpandPath(prefix + binSuffix)}
}

func IsWindows() bool {
	return false
}

func HasLongPathSupport() bool {
	return true
}

func EnforceLongpathSupport() error {
	return nil
}
