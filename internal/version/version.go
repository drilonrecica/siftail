package version

import "runtime/debug"

var (
	Version   = "dev"
	Commit    = "unknown"
	BuildDate = "unknown"
)

func GoVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	return info.GoVersion
}
