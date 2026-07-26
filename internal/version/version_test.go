package version

import "testing"

// A service binary must answer --version and exit. panel-agent used to ignore
// its arguments entirely, so `panel-agent --version` started the daemon and
// hung — the first command an operator tries.
func TestHandleFlag(t *testing.T) {
	for _, arg := range []string{"-v", "--version", "version", "-h", "--help", "help"} {
		if !HandleFlag([]string{arg}, "panel-agent", "summary") {
			t.Errorf("%q should stop the process", arg)
		}
	}
	for _, args := range [][]string{{}, {"serve"}, {"--socket", "/tmp/x"}} {
		if HandleFlag(args, "panel-agent", "summary") {
			t.Errorf("%v should NOT stop the process", args)
		}
	}
}
