package api

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// TOTP (RFC 6238) implemented in-house — a 2FA panel should not pull a
// dependency for 40 lines of HMAC. 30-second step, 6 digits, SHA-1 (what
// every authenticator app expects).

// newTOTPSecret returns a base32 secret suitable for authenticator apps.
func newTOTPSecret() string {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return strings.TrimRight(base32.StdEncoding.EncodeToString(b), "=")
}

// totpURI builds the otpauth:// provisioning URI (QR-encodable).
func totpURI(secret, account, issuer string) string {
	v := url.Values{}
	v.Set("secret", secret)
	v.Set("issuer", issuer)
	v.Set("algorithm", "SHA1")
	v.Set("digits", "6")
	v.Set("period", "30")
	return fmt.Sprintf("otpauth://totp/%s:%s?%s",
		url.PathEscape(issuer), url.PathEscape(account), v.Encode())
}

// totpCode computes the 6-digit code for a secret at a given counter.
func totpCode(secret string, counter uint64) (string, bool) {
	key, err := base32.StdEncoding.DecodeString(strings.ToUpper(padBase32(secret)))
	if err != nil {
		return "", false
	}
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], counter)
	mac := hmac.New(sha1.New, key)
	mac.Write(buf[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	code := (uint32(sum[offset]&0x7f)<<24 |
		uint32(sum[offset+1])<<16 |
		uint32(sum[offset+2])<<8 |
		uint32(sum[offset+3])) % 1_000_000
	return fmt.Sprintf("%06d", code), true
}

// verifyTOTP checks a code against the current 30s window ±1 step, so a
// code entered just before/after a boundary still works.
func verifyTOTP(secret, code string, now time.Time) bool {
	code = strings.TrimSpace(code)
	if len(code) != 6 {
		return false
	}
	step := uint64(now.Unix() / 30)
	for _, c := range []uint64{step - 1, step, step + 1} {
		if want, ok := totpCode(secret, c); ok && subtleEqual(want, code) {
			return true
		}
	}
	return false
}

func padBase32(s string) string {
	if m := len(s) % 8; m != 0 {
		s += strings.Repeat("=", 8-m)
	}
	return s
}

// subtleEqual is a length-safe constant-time-ish compare for short codes.
func subtleEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var v byte
	for i := 0; i < len(a); i++ {
		v |= a[i] ^ b[i]
	}
	return v == 0
}
