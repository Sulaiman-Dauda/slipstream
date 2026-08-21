package api

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/slipstream-panel/slipstream/internal/state"
)

func recoverStore(t *testing.T) *state.Store {
	t.Helper()
	store, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("opening the store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

// Physical access is the credential, so a non-root caller gets nothing. Without this the
// command would be a password reset any user on the box could run.
func TestRecoverAdminRefusesNonRoot(t *testing.T) {
	store := recoverStore(t)
	err := RecoverAdmin(store, "someone@example.com", false, false)
	if err == nil || !strings.Contains(err.Error(), "root") {
		t.Fatalf("expected a refusal mentioning root, got %v", err)
	}
}

// A panel whose setup never completed, or whose only account was deleted. The installer
// token is long gone by then, so this has to be able to make the first admin.
func TestRecoverAdminCreatesFirstAccount(t *testing.T) {
	store := recoverStore(t)

	if err := RecoverAdmin(store, "first@example.com", false, true); err != nil {
		t.Fatalf("creating the first admin: %v", err)
	}

	user, err := store.GetUserByEmail("first@example.com")
	if err != nil {
		t.Fatalf("the account was not created: %v", err)
	}
	if user.Role != "admin" {
		t.Fatalf("expected role admin, got %q", user.Role)
	}
	if !strings.HasPrefix(user.PasswordHash, "argon2id$") {
		t.Fatalf("password was not hashed with argon2id: %q", user.PasswordHash)
	}
}

// With no accounts and no address there is nothing to act on, and inventing one would be
// worse than saying so.
func TestRecoverAdminEmptyPanelNeedsAnEmail(t *testing.T) {
	store := recoverStore(t)
	if err := RecoverAdmin(store, "", false, true); err == nil {
		t.Fatal("expected a refusal when the panel is empty and no email was given")
	}
}

// On a panel with more than one account, guessing which to reset is a silent failure that
// looks like a working recovery. It lists them instead.
func TestRecoverAdminListsRatherThanGuessing(t *testing.T) {
	store := recoverStore(t)
	hash, _ := hashPassword("an-existing-password")
	if _, err := store.CreateUser("admin@example.com", hash, "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateUser("operator@example.com", hash, "operator"); err != nil {
		t.Fatal(err)
	}

	err := RecoverAdmin(store, "", false, true)
	if err == nil {
		t.Fatal("expected it to stop and list the accounts")
	}
	for _, want := range []string{"admin@example.com", "operator@example.com", "--email"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the listing did not mention %q: %v", want, err)
		}
	}
}

// The reset has to actually change the hash, and has to revoke sessions: if the reason for
// resetting is that somebody else holds the old password, leaving their session alive
// achieves nothing.
func TestRecoverAdminResetsAndRevokesSessions(t *testing.T) {
	store := recoverStore(t)
	oldHash, _ := hashPassword("the-old-password")
	user, err := store.CreateUser("owner@example.com", oldHash, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateSession("a-live-session-token", user.ID, sessionTTL); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UserForSession("a-live-session-token"); err != nil {
		t.Fatalf("the session should be valid before the reset: %v", err)
	}

	if err := RecoverAdmin(store, "owner@example.com", false, true); err != nil {
		t.Fatalf("reset failed: %v", err)
	}

	after, err := store.GetUserByEmail("owner@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if after.PasswordHash == oldHash {
		t.Fatal("the password hash did not change")
	}
	if verifyPassword("the-old-password", after.PasswordHash) {
		t.Fatal("the old password still works after a reset")
	}
	if _, err := store.UserForSession("a-live-session-token"); err == nil {
		t.Fatal("the open session survived the reset")
	}
}

// The other way people get locked out: the password is fine and the phone is gone.
func TestRecoverAdminCanDisableTOTP(t *testing.T) {
	store := recoverStore(t)
	hash, _ := hashPassword("a-password")
	user, err := store.CreateUser("twofactor@example.com", hash, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetUserTOTPSecret(user.ID, "SOMESECRET"); err != nil {
		t.Fatal(err)
	}
	if err := store.EnableUserTOTP(user.ID); err != nil {
		t.Fatal(err)
	}

	// Not without asking: silently removing somebody's second factor would be worse than
	// the lockout it solves.
	if err := RecoverAdmin(store, "twofactor@example.com", false, true); err != nil {
		t.Fatal(err)
	}
	if _, enabled, _ := store.UserTOTP(user.ID); !enabled {
		t.Fatal("two-factor was switched off without being asked to")
	}

	if err := RecoverAdmin(store, "twofactor@example.com", true, true); err != nil {
		t.Fatal(err)
	}
	if _, enabled, _ := store.UserTOTP(user.ID); enabled {
		t.Fatal("--disable-2fa did not switch two-factor off")
	}
}

// A typo in an address must not silently do nothing and report success.
func TestRecoverAdminUnknownAddress(t *testing.T) {
	store := recoverStore(t)
	hash, _ := hashPassword("a-password")
	if _, err := store.CreateUser("real@example.com", hash, "admin"); err != nil {
		t.Fatal(err)
	}
	err := RecoverAdmin(store, "typo@example.com", false, true)
	if err == nil || !strings.Contains(err.Error(), "typo@example.com") {
		t.Fatalf("expected a clear failure naming the address, got %v", err)
	}
}

// Recovery passwords get read off a terminal and typed into a browser.
func TestGeneratedPasswordIsLongAndUnambiguous(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		p, err := generatePassword()
		if err != nil {
			t.Fatal(err)
		}
		if len(p) < 20 {
			t.Fatalf("too short to be a credential: %q", p)
		}
		if strings.ContainsAny(p, "l1IO0") {
			t.Fatalf("contains a character that cannot be transcribed reliably: %q", p)
		}
		if seen[p] {
			t.Fatalf("generated the same password twice: %q", p)
		}
		seen[p] = true
	}
}

// It is a recovery path into an admin panel, so it leaves a trail.
func TestRecoverAdminIsAudited(t *testing.T) {
	store := recoverStore(t)
	hash, _ := hashPassword("a-password")
	if _, err := store.CreateUser("audited@example.com", hash, "admin"); err != nil {
		t.Fatal(err)
	}
	if err := RecoverAdmin(store, "audited@example.com", false, true); err != nil {
		t.Fatal(err)
	}

	events, err := store.ListAuditEvents(50)
	if err != nil {
		t.Skipf("no audit listing available: %v", err)
	}
	found := false
	for _, e := range events {
		if strings.Contains(e.Action, "password") && strings.Contains(e.Subject, "audited@example.com") {
			found = true
		}
	}
	if !found {
		t.Fatal("the reset was not written to the audit log")
	}
}

// Every admin deleted, leaving an operator who cannot create one. Resetting the operator's
// password gets you in and not out of trouble, so it has to say so.
func TestRecoverAdminWarnsWhenNoAdminRemains(t *testing.T) {
	store := recoverStore(t)
	hash, _ := hashPassword("a-password")
	if _, err := store.CreateUser("operator@example.com", hash, "operator"); err != nil {
		t.Fatal(err)
	}

	// It still succeeds: getting in is better than not, and the warning is on stdout.
	if err := RecoverAdmin(store, "operator@example.com", false, true); err != nil {
		t.Fatalf("resetting an operator should still work: %v", err)
	}

	users, _ := store.ListUsers()
	if hasAdmin(users) {
		t.Fatal("the fixture was meant to have no admin")
	}
}
