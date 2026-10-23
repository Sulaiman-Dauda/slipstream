package api

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/slipstream-panel/slipstream/internal/rpc"
)

const testWPConfig = `<?php
define( 'DB_NAME', 'shop_db' );
define( 'DB_USER', 'shop_user' );
define( 'DB_PASSWORD', 'hunter2-db-password' );
define('AUTH_KEY',         'k3y-with-\'quote\'-inside');
define("SLIPSTREAM_SITE_TOKEN", "site-token-value");
define( 'STRIPE_SECRET', 'sk_live_value' );
define( 'WP_DEBUG', false );
$table_prefix = 'wp_';
`

var testWPConfigSecrets = []string{"hunter2-db-password", "k3y-with", "site-token-value", "sk_live_value", "shop_user"}

// readOnlyClient creates a site and a read-only account, and returns the
// admin, the read-only client and the site id.
func readOnlyClient(t *testing.T) (*client, *client, *fakeAgent, int64) {
	t.Helper()
	s, agent, ts := testServer(t)
	admin := setupAdmin(t, s, ts)
	if resp, body := admin.do("POST", "/api/sites", map[string]any{"domain": "shop.example.com", "type": "static"}); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("create site = %d %s", resp.StatusCode, body)
	}
	waitForTasks(t, s)
	site, err := s.Store.GetSiteByDomain("shop.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if resp, body := admin.do("POST", "/api/users", map[string]any{
		"email": "viewer@example.com", "password": "viewer-password!", "role": "readonly",
	}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("create readonly = %d %s", resp.StatusCode, body)
	}
	ro := &client{t: t, base: ts.URL}
	if resp, body := ro.do("POST", "/api/login", map[string]string{"email": "viewer@example.com", "password": "viewer-password!"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("readonly login = %d %s", resp.StatusCode, body)
	}
	return admin, ro, agent, site.ID
}

func fileURL(id int64, op, rel string) string {
	return "/api/sites/" + strconv.FormatInt(id, 10) + "/files/" + op + "?path=" + url.QueryEscape(rel)
}

// A read-only account can browse a site's files, but wp-config.php holds the
// database password and the connector's site token, and a database dump holds
// every customer record. Read-only must not mean "can read every secret".
func TestReadOnlyCannotReadSecrets(t *testing.T) {
	admin, ro, agent, id := readOnlyClient(t)
	agent.results[rpc.MethodReadFile] = rpc.ReadFileResult{RelPath: "shared/wp-config.php", Content: testWPConfig}

	for _, rel := range []string{"shared/wp-config.php", "current/wp-config.php", "/shared/../shared/wp-config.php", "shared/wp-config.php.bak"} {
		resp, body := ro.do("GET", fileURL(id, "read", rel), nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("readonly read %s = %d %s, want 200 with values hidden", rel, resp.StatusCode, body)
		}
		for _, secret := range testWPConfigSecrets {
			if strings.Contains(string(body), secret) {
				t.Errorf("readonly read %s exposed %q", rel, secret)
			}
		}
		for _, kept := range []string{"DB_NAME", "shop_db", "WP_DEBUG", "table_prefix"} {
			if !strings.Contains(string(body), kept) {
				t.Errorf("readonly read %s lost the harmless %q", rel, kept)
			}
		}
	}

	agent.results[rpc.MethodReadFile] = rpc.ReadFileResult{RelPath: ".env", Content: "APP_KEY=base64:abc\nDB_PASSWORD=env-db-pass\n# comment\nEMPTY=\n"}
	resp, body := ro.do("GET", fileURL(id, "read", "current/.env.production"), nil)
	if resp.StatusCode != http.StatusOK || strings.Contains(string(body), "env-db-pass") || strings.Contains(string(body), "base64:abc") {
		t.Errorf("readonly read .env = %d %s, want values hidden", resp.StatusCode, body)
	}

	// Dumps, exports and keys have nothing harmless left once hidden.
	agent.calls = nil
	for _, rel := range []string{
		"logs/db-latest.sql", "shared/exports/shop_db-20261015-120000.sql", "backup.sql.gz",
		"shared/wp-config.php", ".htpasswd", "certs/site.key", ".ssh/id_ed25519",
	} {
		for _, op := range []string{"read", "download"} {
			if op == "read" && strings.Contains(rel, "wp-config") {
				continue // masked above, not refused
			}
			resp, body := ro.do("GET", fileURL(id, op, rel), nil)
			if resp.StatusCode != http.StatusForbidden {
				t.Errorf("readonly %s %s = %d %s, want 403", op, rel, resp.StatusCode, body)
			}
		}
	}
	if agent.called(rpc.MethodTransferFile) {
		t.Error("a refused download still reached the agent")
	}

	// Ordinary files are untouched for read-only, and admins see everything.
	agent.results[rpc.MethodReadFile] = rpc.ReadFileResult{RelPath: "current/index.php", Content: "<?php define('WP_USE_THEMES', true);"}
	if resp, body := ro.do("GET", fileURL(id, "read", "current/index.php"), nil); resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "WP_USE_THEMES") {
		t.Errorf("readonly read index.php = %d %s", resp.StatusCode, body)
	}
	agent.results[rpc.MethodReadFile] = rpc.ReadFileResult{RelPath: "shared/wp-config.php", Content: testWPConfig}
	resp, body = admin.do("GET", fileURL(id, "read", "shared/wp-config.php"), nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "hunter2-db-password") {
		t.Errorf("admin read wp-config = %d %s, want the real file", resp.StatusCode, body)
	}
	if resp, body := admin.do("GET", fileURL(id, "download", "logs/db-latest.sql"), nil); resp.StatusCode != http.StatusOK {
		t.Errorf("admin download dump = %d %s", resp.StatusCode, body)
	}
}

func TestMaskWPConfigKeepsItValidPHP(t *testing.T) {
	got := maskWPConfig(testWPConfig)
	if strings.Count(got, "define(") != strings.Count(testWPConfig, "define(") {
		t.Fatalf("defines lost:\n%s", got)
	}
	if !strings.Contains(got, `define( 'DB_PASSWORD', '********' );`) {
		t.Errorf("DB_PASSWORD not masked in place:\n%s", got)
	}
	if !strings.Contains(got, `define("SLIPSTREAM_SITE_TOKEN", "********");`) {
		t.Errorf("double-quoted define not masked in place:\n%s", got)
	}
}

// Hand-written and migrated configs use every PHP shape there is. Anything
// quoted that is not a known-harmless value is hidden.
func TestMaskWPConfigShapes(t *testing.T) {
	in := `<?php
// Don't edit below. That's all, stop editing!
/* It's here */
# it's a comment
define( 'DB_NAME', 'shop_db' );
define('DB_PASSWORD', getenv('DB_PASSWORD') ?: 'fallback-sekret');
$pw = 'varval-sekret'; define('DB_USER', $pw);
DEFINE('AUTH_KEY','upper-sekret');
const STRIPE_KEY = 'const-sekret';
define('LOGGED_IN_KEY', 'aaa' . 'concat-sekret');
define('NONCE_KEY', <<<EOT
heredoc-sekret
EOT);
define('X', <<<'EOT'
nowdoc-sekret
EOT
);
$table_prefix = 'wp_';
if ( ! defined( 'ABSPATH' ) ) { define( 'ABSPATH', __DIR__ . '/' ); }
require_once ABSPATH . 'wp-settings.php';
`
	got := maskWPConfig(in)
	if strings.Contains(got, "sekret") {
		t.Errorf("a secret survived masking:\n%s", got)
	}
	for _, kept := range []string{"'DB_NAME', 'shop_db'", "'DB_PASSWORD'", "$table_prefix = 'wp_'", "That's all", "EOT", "defined( 'ABSPATH' )", "ABSPATH . 'wp-settings.php'"} {
		if !strings.Contains(got, kept) {
			t.Errorf("lost %q:\n%s", kept, got)
		}
	}
}

func TestMaskEnvMultiline(t *testing.T) {
	in := "A=1\nKEY=\"-----BEGIN KEY-----\nMIIBsekret==\n-----END KEY-----\"\nB='x'\nC=\"ends-escaped\\\"\nMIIBtwo\"\n"
	got := maskEnv(in)
	if strings.Contains(got, "MIIB") || strings.Contains(got, "BEGIN") {
		t.Errorf("multi-line value survived:\n%s", got)
	}
	if !strings.Contains(got, "B=") || !strings.Contains(got, "A=") {
		t.Errorf("keys lost:\n%s", got)
	}
}

func TestClassifyArchivesAndCredentialFiles(t *testing.T) {
	for _, rel := range []string{
		"migration-upload.tar.gz", "backup_2026-db.gz", "site.wpress", "files.zip",
		"current/.git/config", ".my.cnf", ".pgpass", ".netrc", "current/.npmrc",
	} {
		if classifyFile(rel) != fileWithheld {
			t.Errorf("%s = %v, want withheld", rel, classifyFile(rel))
		}
	}
	for _, rel := range []string{"current/index.php", "current/wp-content/debug.log", "current/style.css"} {
		if classifyFile(rel) != fileOrdinary {
			t.Errorf("%s = %v, want ordinary", rel, classifyFile(rel))
		}
	}
}

// Shapes found in the second review: comments that look like code, CR line
// endings, inline text outside PHP, numeric passwords, commented-out env
// lines and YAML-style env lines.
func TestMaskSecondReviewShapes(t *testing.T) {
	for name, in := range map[string]string{ //nolint:gosec // G101: fake credentials the masker must hide
		"comment before var": "<?php\n// Redis: include the password below\n$redis_pass = 'sekret1';\n",
		"cr endings":         "<?php\r// db\rdefine('DB_PASSWORD','sekret2');\r",
		"hash bracket":       "<?php\n#[todo] don't forget\ndefine('DB_PASSWORD','sekret3');\n",
		"inline text":        "<?php ?>\nDon't edit\n<?php define('DB_PASSWORD','sekret4');\n",
		"comment ends at ?>": "<?php // it's ?> text <?php define('DB_PASSWORD','sekret5');\n",
		"numeric password":   "<?php define('DB_PASSWORD', 12345678); define('WP_POST_REVISIONS', 5);\n",
	} {
		got := maskWPConfig(in)
		if strings.Contains(got, "sekret") || strings.Contains(got, "12345678") {
			t.Errorf("%s: a secret survived:\n%s", name, got)
		}
		if name == "numeric password" && !strings.Contains(got, "'WP_POST_REVISIONS', 5") {
			t.Errorf("harmless number lost:\n%s", got)
		}
	}
	env := maskEnv("# DB_PASSWORD=old-sekret\n# a note\nURL: postgres://u:sekret@db/x?ssl=on\nK=`line1\nMIIB==sekret\n`\nexport A=sekret\n")
	for _, leak := range []string{"sekret", "MIIB"} {
		if strings.Contains(env, leak) {
			t.Errorf("env %q survived:\n%s", leak, env)
		}
	}
	for _, kept := range []string{"# DB_PASSWORD=", "# a note", "export A="} {
		if !strings.Contains(env, kept) {
			t.Errorf("env lost %q:\n%s", kept, env)
		}
	}
	for rel, want := range map[string]fileSecrecy{
		".wp-config.php.swp": fileWithheld, "backup-wp-config.php": fileMasked,
		".envrc": fileMasked, "prod.env": fileMasked, "wp-config-sample.php": fileOrdinary,
	} {
		if got := classifyFile(rel); got != want {
			t.Errorf("classify %s = %v, want %v", rel, got, want)
		}
	}
}
