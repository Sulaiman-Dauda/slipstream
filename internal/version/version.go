// Package version exposes the build version, injected at link time.
package version

import "fmt"

// Version is set via -ldflags at build time.
var Version = "dev"

// HandleFlag prints version or usage information and reports whether the
// process should exit now. Daemons call it before doing any work: `--version`
// on a service binary must answer and exit, not start listening. (panel-agent
// previously ignored its arguments entirely, so `panel-agent --version`
// launched the daemon — the first thing an operator types, and it hung.)
func HandleFlag(args []string, name, summary string) bool {
	for _, a := range args {
		switch a {
		case "-v", "--version", "version":
			fmt.Printf("%s %s\n", name, Version)
			return true
		case "-h", "--help", "help":
			fmt.Printf("%s %s\n\n%s\n", name, Version, summary)
			return true
		}
	}
	return false
}
