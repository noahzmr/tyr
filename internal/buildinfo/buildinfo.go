// Package buildinfo carries version data injected with -ldflags -X.
package buildinfo

import "runtime/debug"

var (
	Version = "dev"
	Commit  = ""
	Date    = ""
)

func init() {
	if Commit != "" {
		return
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				Commit = s.Value
			case "vcs.time":
				Date = s.Value
			}
		}
	}
}
