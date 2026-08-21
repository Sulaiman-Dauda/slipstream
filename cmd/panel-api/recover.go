package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/slipstream-panel/slipstream/internal/api"
	"github.com/slipstream-panel/slipstream/internal/state"
)

// runRecoverAdmin is the host-side way back into the panel.
//
// It opens the state database directly rather than going through the API, because the
// whole point is that it works when the API will not let you in: no session, no password,
// no second factor, or a panel that is not even running.
func runRecoverAdmin(statePath string, args []string) error {
	fs := flag.NewFlagSet("recover-admin", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	email := fs.String("email", "", "the account to reset, or to create if the panel has none")
	disable2FA := fs.Bool("disable-2fa", false, "also switch off two-factor, for a lost device")

	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `panel-api recover-admin: regain access to the panel from the host.

Run as root. Physical access to the server is the credential, which is the same
trust boundary every other recovery mechanism uses: anyone with root can already
read the database and replace the binaries.

  panel-api recover-admin                              list the accounts
  panel-api recover-admin --email you@example.com      reset that one
  panel-api recover-admin --email you@example.com --disable-2fa

The password is generated and printed once. It is never taken as an argument,
because that would put it in the shell history and in ps for every user on the box.
Resetting a password also revokes that account's open sessions.

`)
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	store, err := state.Open(statePath)
	if err != nil {
		return fmt.Errorf("opening %s: %w", statePath, err)
	}
	defer store.Close()

	return api.RecoverAdmin(store, *email, *disable2FA, os.Geteuid() == 0)
}
