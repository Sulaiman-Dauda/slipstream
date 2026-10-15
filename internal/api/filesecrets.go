package api

import (
	"net/http"
	"path"
	"regexp"
	"strings"
)

// Read-only accounts can browse every site's files, but a few files hold
// what would let them stop being read-only: wp-config.php has the database
// password and the connector's site token, and a dump in logs/ or
// shared/exports/ holds every customer record. Those are masked or refused
// for anyone who cannot manage the site. Admins and operators already reach
// the same data through the SQL console and file editor, so they are not
// restricted here.
//
// The check is on the requested name, so a site user could plant a symlink
// under an innocent name pointing at, say, the root-owned logs/db-latest.sql.
// That gains them nothing: the site user can already read wp-config.php and
// dump the same database with its credentials.

type fileSecrecy int

const (
	fileOrdinary fileSecrecy = iota
	fileMasked               // readable with secret values replaced
	fileWithheld             // nothing harmless left once the secrets go
)

const maskedValue = "********"

const withheldMessage = "this file holds credentials or customer data, which read-only accounts cannot open"

// secrecyFor is how secret rel is for this caller: always ordinary for
// accounts that can manage sites.
func (s *Server) secrecyFor(r *http.Request, rel string) fileSecrecy {
	if user, err := s.sessionUser(r); err == nil && (user.Role == "admin" || user.Role == "operator") {
		return fileOrdinary
	}
	return classifyFile(rel)
}

func classifyFile(rel string) fileSecrecy {
	p := path.Clean("/" + rel)
	base := strings.ToLower(path.Base(p))
	switch {
	case strings.HasPrefix(p, "/shared/exports/"), strings.HasPrefix(p, "/.ssh/"),
		strings.Contains(p+"/", "/.git/"):
		return fileWithheld
	case strings.Contains(base, ".sql"), // .sql, .sql.gz, db-latest.sql
		hasAnySuffix(base, ".gz", ".tgz", ".tar", ".zip", ".bz2", ".xz", ".zst", ".7z", ".rar", ".wpress"),
		hasAnySuffix(base, ".pem", ".key", ".p12", ".pfx"),
		base == ".htpasswd", base == "auth.json", base == ".my.cnf", base == ".pgpass",
		base == ".netrc", base == ".npmrc", base == ".git-credentials",
		strings.HasPrefix(base, "id_rsa"), strings.HasPrefix(base, "id_ed25519"), strings.HasPrefix(base, "id_ecdsa"),
		// An editor's swap file of a config is binary and holds it whole.
		strings.Contains(base, "wp-config") && hasAnySuffix(base, ".swp", ".swo"):
		// Archives too: a migration upload or a plugin's backup carries
		// wp-config.php and usually a dump.
		return fileWithheld
	case strings.Contains(base, "wp-config") && base != "wp-config-sample.php",
		base == ".env", base == ".envrc", strings.HasPrefix(base, ".env."), strings.HasSuffix(base, ".env"):
		return fileMasked
	}
	return fileOrdinary
}

func hasAnySuffix(s string, suffixes ...string) bool {
	for _, x := range suffixes {
		if strings.HasSuffix(s, x) {
			return true
		}
	}
	return false
}

func maskFile(rel, content string) string {
	base := strings.ToLower(path.Base(path.Clean("/" + rel)))
	if strings.HasPrefix(base, ".env") || strings.HasSuffix(base, ".env") {
		return maskEnv(content)
	}
	return maskWPConfig(content)
}

// wpConfigHarmless are the string defines worth seeing when diagnosing a
// site. Every other string define is masked, so a plugin's API key under a
// name nobody anticipated is hidden by default rather than shown.
var wpConfigHarmless = map[string]bool{
	"DB_NAME": true, "DB_HOST": true, "DB_CHARSET": true, "DB_COLLATE": true,
	"WP_HOME": true, "WP_SITEURL": true, "WP_ENVIRONMENT_TYPE": true,
	"WP_CONTENT_DIR": true, "WP_CONTENT_URL": true, "WP_PLUGIN_DIR": true, "WP_PLUGIN_URL": true,
	"WP_MEMORY_LIMIT": true, "WP_MAX_MEMORY_LIMIT": true, "WP_DEBUG_LOG": true,
	"FS_METHOD": true, "COOKIE_DOMAIN": true, "SLIPSTREAM_ENDPOINT": true,
}

var (
	defineNameRe   = regexp.MustCompile(`(?i)(\bdefined?\s*\(|\b(require|include)(_once)?\b[^;'"\n]*)\s*$`)
	defineValueRe  = regexp.MustCompile(`(?i)\bdefine\s*\(\s*['"]([A-Za-z0-9_]+)['"]\s*,\s*$`)
	tablePrefixRe  = regexp.MustCompile(`\$table_prefix\s*=\s*$`)
	heredocStartRe = regexp.MustCompile(`^<<<[ \t]*(['"]?)([A-Za-z_][A-Za-z0-9_]*)['"]?\r?\n`)
	secretNameRe   = regexp.MustCompile(`(?i)pass|key|salt|secret|token|auth`)
	numberRe       = regexp.MustCompile(`^[0-9][0-9A-Za-z_.]*`)
)

// maskWPConfig hides every string literal and heredoc in a PHP file except
// define names, required paths, the values of wpConfigHarmless defines and
// $table_prefix, plus numbers given to a define named like a secret.
// Masking literals rather than matching define shapes means getenv
// fallbacks, variables, const, concatenation and DEFINE in capitals are all
// covered. Comments and text outside <?php ... ?> are skipped whole, so an
// apostrophe in "That's all, stop editing!" cannot shift where a string
// starts, and the look-back that decides what to keep reads code only.
func maskWPConfig(s string) string {
	var b, code strings.Builder
	emit := func(t string, isCode bool) {
		b.WriteString(t)
		if isCode {
			code.WriteString(t)
		} else {
			code.WriteByte(' ')
		}
	}
	lookBack := func() string {
		c := code.String()
		return c[max(0, len(c)-200):]
	}
	inPHP := false
	for i := 0; i < len(s); {
		rest := s[i:]
		if !inPHP {
			n := strings.Index(rest, "<?")
			if n < 0 {
				n = len(rest)
			}
			emit(rest[:n], false)
			i += n
			inPHP = true
			continue
		}
		switch {
		case strings.HasPrefix(rest, "?>"):
			emit("?>", true)
			i += 2
			inPHP = false
		case strings.HasPrefix(rest, "//"), rest[0] == '#':
			// PHP ends a line comment at a newline, a CR or ?>.
			n := strings.IndexAny(rest, "\r\n")
			if n < 0 {
				n = len(rest)
			}
			if q := strings.Index(rest[:n], "?>"); q >= 0 {
				n = q
			}
			emit(rest[:n], false)
			i += n
		case strings.HasPrefix(rest, "/*"):
			n := strings.Index(rest[2:], "*/")
			if n < 0 {
				n = len(rest)
			} else {
				n += 4
			}
			emit(rest[:n], false)
			i += n
		case strings.HasPrefix(rest, "<<<"):
			m := heredocStartRe.FindStringSubmatchIndex(rest)
			if m == nil {
				emit("<<<", true)
				i += 3
				continue
			}
			label := rest[m[4]:m[5]]
			emit(rest[:m[1]], true)
			i += m[1]
			emit(maskedValue+"\n", true)
			i += heredocBodyLen(s[i:], label)
		case rest[0] == '\'' || rest[0] == '"':
			n := literalLen(rest)
			if n < 0 {
				n = len(rest)
			}
			window := lookBack()
			keep := defineNameRe.MatchString(window) || tablePrefixRe.MatchString(window)
			if m := defineValueRe.FindStringSubmatch(window); m != nil && wpConfigHarmless[strings.ToUpper(m[1])] {
				keep = true
			}
			if keep {
				emit(rest[:n], true)
			} else {
				emit(rest[:1]+maskedValue+rest[:1], true)
			}
			i += n
		case rest[0] >= '0' && rest[0] <= '9' && (i == 0 || !isIdentByte(s[i-1])):
			n := len(numberRe.FindString(rest))
			if m := defineValueRe.FindStringSubmatch(lookBack()); m != nil &&
				!wpConfigHarmless[strings.ToUpper(m[1])] && secretNameRe.MatchString(m[1]) {
				emit("'"+maskedValue+"'", true)
			} else {
				emit(rest[:n], true)
			}
			i += n
		default:
			emit(rest[:1], true)
			i++
		}
	}
	return b.String()
}

// literalLen is the length of the quoted literal at the start of s,
// closing quote included, or -1 if it never closes.
func literalLen(s string) int {
	for j := 1; j < len(s); j++ {
		switch s[j] {
		case '\\':
			j++
		case s[0]:
			return j + 1
		}
	}
	return -1
}

// heredocBodyLen is how far the body runs before its closing label line
// (PHP 7.3+ allows it indented and followed by more code), or all of s.
func heredocBodyLen(s, label string) int {
	for off := 0; off < len(s); {
		line := s[off:]
		if n := strings.IndexByte(line, '\n'); n >= 0 {
			line = line[:n]
		}
		t := strings.TrimLeft(line, " \t")
		if strings.HasPrefix(t, label) && (len(t) == len(label) || !isIdentByte(t[len(label)])) {
			return off
		}
		off += len(line) + 1
	}
	return len(s)
}

func isIdentByte(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

var envKeyRe = regexp.MustCompile(`^\s*(export\s+)?[A-Za-z_][A-Za-z0-9_.-]*\s*$`)

// maskEnv hides every value in a dotenv file, including the continuation
// lines of a quoted value that spans several (a PEM key), commented-out
// assignments (an old password left as "# DB_PASSWORD=..."), and any line
// that is not KEY=value, such as the YAML-style "KEY: value".
func maskEnv(s string) string {
	lines := strings.Split(s, "\n")
	var open byte
	for i, line := range lines {
		if open != 0 {
			if strings.IndexByte(line, open) >= 0 {
				open = 0
			}
			lines[i] = maskedValue
			continue
		}
		t := strings.TrimSpace(line)
		if t == "" {
			continue
		}
		if strings.HasPrefix(t, "#") {
			if k, _, ok := strings.Cut(line, "="); ok {
				lines[i] = k + "=" + maskedValue
			}
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || !envKeyRe.MatchString(k) {
			lines[i] = maskedValue
			continue
		}
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if strings.IndexByte("\"'`", v[0]) >= 0 && literalLen(v) < 0 {
			open = v[0]
		}
		lines[i] = k + "=" + maskedValue
	}
	return strings.Join(lines, "\n")
}
