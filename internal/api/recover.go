package api

import (
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"os"
	"strings"

	"github.com/slipstream-panel/slipstream/internal/state"
)

// RecoverAdmin is the way back in for whoever owns the machine.
//
// Until this existed, losing the panel password meant losing the panel: there was no
// reset, no user creation from the host, and the only password endpoint needed a session
// you could not get. Root on the box could read the database holding the accounts and
// could not do anything with it. Every comparable panel has this, because sooner or later
// somebody loses a password or a phone.
//
// **Physical access to the server is the credential.** That is the same trust boundary
// every other recovery mechanism uses, and it is not a weakening: anyone with root can
// already read the database, replace the binaries and read the TLS keys. Refusing to help
// them did not make the panel safer, it only made it brittle.
//
// The password is generated rather than accepted as an argument. A password on a command
// line is in the shell history and in the output of ps for every user on the box, which
// would trade one recovery problem for a disclosure one.
func RecoverAdmin(store *state.Store, email string, disable2FA bool, isRoot bool) error {
	if !isRoot {
		return errors.New("must be run as root: physical access to the server is the credential")
	}

	users, err := store.ListUsers()
	if err != nil {
		return fmt.Errorf("reading accounts: %w", err)
	}

	// Nothing to recover: this is a panel whose setup never completed, or whose only
	// account was deleted. Make the first admin rather than sending them to an installer
	// token that was consumed months ago.
	if len(users) == 0 {
		if email == "" {
			return errors.New("no accounts exist: pass --email to create the first admin")
		}
		password, err := generatePassword()
		if err != nil {
			return err
		}
		hash, err := hashPassword(password)
		if err != nil {
			return err
		}
		user, err := store.CreateUser(email, hash, "admin")
		if err != nil {
			return fmt.Errorf("creating the account: %w", err)
		}
		_ = store.Audit("root (console)", "user.create", email, "first admin created by host recovery")
		report(user.Email, password, "created")
		return nil
	}

	// Accounts exist and no email was given. Do not guess which one: on a panel with an
	// operator and an admin, resetting the wrong one is a silent failure that looks like
	// a working recovery.
	if email == "" {
		var b strings.Builder
		b.WriteString("no --email given. Accounts on this panel:\n")
		for _, u := range users {
			b.WriteString(fmt.Sprintf("  %-40s %s\n", u.Email, u.Role))
		}
		b.WriteString("\nRe-run with --email <address> to reset one of them.")
		return errors.New(b.String())
	}

	target, err := store.GetUserByEmail(email)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return fmt.Errorf("no account with the address %q. Run without --email to list them", email)
		}
		return err
	}

	password, err := generatePassword()
	if err != nil {
		return err
	}
	hash, err := hashPassword(password)
	if err != nil {
		return err
	}

	// This also deletes every session that account has open, which is what a password
	// reset has to do: if the reason for the reset is that somebody else had the old one,
	// leaving their session alive achieves nothing.
	if err := store.UpdatePassword(target.ID, hash); err != nil {
		return fmt.Errorf("setting the password: %w", err)
	}

	notes := []string{}

	// A reset alone does not help if the second factor is on a phone that is gone, which
	// is one of the two ways people actually get locked out. Opt-in, because silently
	// removing somebody's 2FA would be worse than the lockout.
	if disable2FA {
		_, enabled, err := store.UserTOTP(target.ID)
		if err != nil {
			return fmt.Errorf("reading two-factor state: %w", err)
		}
		if enabled {
			if err := store.DisableUserTOTP(target.ID); err != nil {
				return fmt.Errorf("disabling two-factor: %w", err)
			}
			notes = append(notes, "two-factor authentication was switched off")
			_ = store.Audit("root (console)", "user.2fa.disable", email, "disabled by host recovery")
		} else {
			notes = append(notes, "two-factor was already off")
		}
	} else {
		if _, enabled, err := store.UserTOTP(target.ID); err == nil && enabled {
			notes = append(notes, "two-factor is still ON for this account: re-run with --disable-2fa if the device is lost")
		}
	}

	// The other lockout nobody thinks about: every admin deleted, leaving operators who
	// cannot add one back. Resetting an operator's password gets you in and not out of
	// trouble, so say so rather than let them find out in the UI.
	if target.Role != "admin" {
		notes = append(notes, fmt.Sprintf("this account is %q, not an admin", target.Role))
	}
	if !hasAdmin(users) {
		notes = append(notes, "there is no admin account on this panel at all: create one from the UI once you are in, or delete the accounts and re-run this to make a fresh admin")
	}

	_ = store.Audit("root (console)", "user.password.reset", email, "reset by host recovery; sessions revoked")
	report(target.Email, password, "reset")
	for _, n := range notes {
		fmt.Fprintf(os.Stdout, "  note: %s\n", n)
	}
	return nil
}

func hasAdmin(users []state.User) bool {
	for _, u := range users {
		if u.Role == "admin" {
			return true
		}
	}
	return false
}

// report prints the credential once, to stdout, and says plainly that it is not stored.
func report(email, password, what string) {
	fmt.Fprintf(os.Stdout, "\n  account:  %s\n  password: %s\n\n", email, password)
	fmt.Fprintf(os.Stdout, "  Password %s. Existing sessions for this account were revoked.\n", what)
	fmt.Fprintf(os.Stdout, "  It is shown once and is not written anywhere. Sign in and change it.\n")
}

// generatePassword returns a long random password from an unambiguous alphabet.
//
// No l/1/I or O/0, because this gets read off a terminal and typed into a browser, and a
// recovery password that cannot be transcribed is not a recovery.
func generatePassword() (string, error) {
	const alphabet = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	const length = 24

	out := make([]byte, length)
	for i := range out {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			return "", fmt.Errorf("generating a password: %w", err)
		}
		out[i] = alphabet[n.Int64()]
	}
	return string(out), nil
}
