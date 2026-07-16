package api

import (
	"testing"
	"time"
)

func TestTOTPRoundTrip(t *testing.T) {
	secret := newTOTPSecret()
	now := time.Now()
	code, ok := totpCode(secret, uint64(now.Unix()/30))
	if !ok {
		t.Fatal("could not compute code")
	}
	if !verifyTOTP(secret, code, now) {
		t.Fatal("current code should verify")
	}
	if verifyTOTP(secret, "000000", now.Add(10*time.Minute)) {
		t.Fatal("wrong code should not verify")
	}
	// A code from the previous window still verifies (±1 step tolerance).
	prev, _ := totpCode(secret, uint64(now.Unix()/30)-1)
	if !verifyTOTP(secret, prev, now) {
		t.Fatal("previous-window code should verify within tolerance")
	}
}

func TestTOTPKnownVector(t *testing.T) {
	// RFC 6238 test vector (SHA-1, secret "12345678901234567890" base32).
	secret := "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	// T=59 → counter 1 → code 287082
	code, ok := totpCode(secret, 1)
	if !ok || code != "287082" {
		t.Fatalf("RFC vector mismatch: got %q ok=%v", code, ok)
	}
}

func TestRateLimiter(t *testing.T) {
	rl := newRateLimiter(3, time.Minute)
	key := "1.2.3.4|a@b.com"
	for i := 0; i < 3; i++ {
		if !rl.allow(key) {
			t.Fatalf("attempt %d should be allowed", i)
		}
	}
	if rl.allow(key) {
		t.Fatal("4th attempt should be blocked")
	}
	rl.reset(key)
	if !rl.allow(key) {
		t.Fatal("after reset, should be allowed again")
	}
}

func TestReadOnlySQLGuard(t *testing.T) {
	ro := []string{"SELECT * FROM x", "  show tables", "DESCRIBE y", "with t as (select 1) select * from t", "select 1;"}
	for _, q := range ro {
		if !isReadOnlySQL(q) {
			t.Errorf("%q should be read-only", q)
		}
	}
	rw := []string{"DELETE FROM x", "drop table y", "UPDATE a SET b=1", "insert into z values(1)"}
	for _, q := range rw {
		if isReadOnlySQL(q) {
			t.Errorf("%q should NOT be read-only", q)
		}
	}
	// The agent runs the query through `mariadb -e`, which executes stacked
	// statements: "select 1; drop table x;" starts with "select " but smuggles
	// a write straight past a prefix-only check. A read-only query must be
	// exactly one statement.
	stacked := []string{
		"select 1; drop table x;",
		"select 1; create table stack_bypass(id int);",
		"show tables; delete from x;",
		"select 1;select 2;",
	}
	for _, q := range stacked {
		if isReadOnlySQL(q) {
			t.Errorf("stacked statement %q should NOT be read-only", q)
		}
	}
}

func TestCronScheduleValidation(t *testing.T) {
	good := []string{"* * * * *", "*/15 * * * *", "0 3 * * 0", "30 2 1 1 *"}
	for _, s := range good {
		if !validCronSchedule(s) {
			t.Errorf("%q should be valid", s)
		}
	}
	bad := []string{"* * * *", "*/15 * * * * *", "rm -rf /", "* * * * * ; reboot", ""}
	for _, s := range bad {
		if validCronSchedule(s) {
			t.Errorf("%q should be invalid", s)
		}
	}
}
