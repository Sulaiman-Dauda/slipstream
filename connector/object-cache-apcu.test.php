<?php
/**
 * Exercises the APCu drop-in outside WordPress.
 *
 * Run it twice, because the two interesting states are the two settings of apc.enable_cli:
 *
 *   php -d apc.enable_cli=1 connector/object-cache-apcu.test.php   shared segment usable
 *   php -d apc.enable_cli=0 connector/object-cache-apcu.test.php   degraded, request only
 *
 * The second one is the case that was broken for the whole life of the drop-in. Every
 * wp-cli invocation runs in it.
 */

$failures = 0;
$checks   = 0;

function check($label, $got, $want) {
    global $failures, $checks;
    $checks++;
    if ($got === $want) { return; }
    $failures++;
    printf("  FAIL %s: got %s, want %s\n", $label, var_export($got, true), var_export($want, true));
}

// The little of WordPress the drop-in touches.
$dir = sys_get_temp_dir() . '/slip-oc-test-' . getmypid();
@mkdir($dir, 0755, true);
define('ABSPATH', $dir . '/');
define('AUTH_KEY', 'a-test-key');
function wp_suspend_cache_addition() { return false; }

// Load a copy, so __DIR__ puts the epoch file in the scratch directory rather than in
// the repository.
$src = __DIR__ . '/object-cache-apcu.php';
copy($src, $dir . '/object-cache.php');
require $dir . '/object-cache.php';

// The drop-in has to say whether it got the shared segment. The old one did not, because
// its guard was a bare return that PHP hoisted straight past, so it installed itself either
// way and nothing downstream could tell the difference.
check('the drop-in publishes whether APCu is in use', defined('SLIPSTREAM_OC_APCU'), true);
$apcu = defined('SLIPSTREAM_OC_APCU') ? SLIPSTREAM_OC_APCU : false;
printf("APCu usable in this process: %s\n", $apcu ? 'yes' : 'no');

$expected = function_exists('apcu_enabled') && apcu_enabled()
    && filter_var((string) ini_get('apc.enable_cli'), FILTER_VALIDATE_BOOLEAN);
if (!function_exists('apcu_fetch')) { $expected = false; }
check('SLIPSTREAM_OC_APCU matches apc.enable_cli', $apcu, $expected);

wp_cache_init();

// ---------------------------------------------------------------------------
// The contract WordPress relies on, which has to hold in both states.
// ---------------------------------------------------------------------------
check('set returns true', wp_cache_set('k1', 'v1'), true);
check('get returns what was set', wp_cache_get('k1'), 'v1');

$found = null;
wp_cache_get('nothing-here', '', false, $found);
check('a miss reports found=false', $found, false);

// This is the one that mattered. add() is WordPress's lock primitive: doing_cron takes it.
// With the old guard, apcu_add() ran against an uninitialised segment on every CLI process
// and returned false, so nothing could ever take a lock.
check('add on a fresh key succeeds', wp_cache_add('lock', 1), true);
check('add on a held key fails', wp_cache_add('lock', 1), false);

check('delete', wp_cache_delete('k1'), true);
check('get after delete', wp_cache_get('k1'), false);

check('incr on a missing key', wp_cache_incr('counter'), false);
wp_cache_set('counter', 5);
check('incr', wp_cache_incr('counter', 3), 8);
check('decr', wp_cache_decr('counter', 2), 6);
check('decr floors at zero', wp_cache_decr('counter', 999), 0);

wp_cache_add_non_persistent_groups(['transient-ish']);
check('non-persistent set', wp_cache_set('np', 'x', 'transient-ish'), true);
check('non-persistent get', wp_cache_get('np', 'transient-ish'), 'x');

// ---------------------------------------------------------------------------
// The epoch: the part that makes a flush cross a process boundary.
// ---------------------------------------------------------------------------
$epoch_file = $dir . '/.slipstream-cache-epoch';
check('an epoch file exists', file_exists($epoch_file), true);
$before = trim(file_get_contents($epoch_file));
check('the epoch is 16 hex characters', (bool) preg_match('/^[0-9a-f]{16}$/', $before), true);

wp_cache_set('survivor', 'yes');

// flush() used to throw here: "APC must be enabled to use APCUIterator".
check('flush returns true', wp_cache_flush(), true);

$after = trim(file_get_contents($epoch_file));
check('flush moved the epoch', $after !== $before, true);

// A cache built after the flush must not see what was written before it. In one process
// that proves the namespace moved; across two processes that same move is what makes
// wp-cli's flush visible to PHP-FPM, which has its own copy of the shared memory and
// cannot be reached any other way.
wp_cache_init();
check('a new instance cannot see pre-flush data', wp_cache_get('survivor'), false);
check('a new instance reuses the flushed epoch', trim(file_get_contents($epoch_file)), $after);

// And it must still be a working cache afterwards.
check('set works after a flush', wp_cache_set('post-flush', 'ok'), true);
check('get works after a flush', wp_cache_get('post-flush'), 'ok');

check('flush_group does not throw', wp_cache_flush_group('default'), true);
check('flush_runtime', wp_cache_flush_runtime(), true);

// ---------------------------------------------------------------------------
array_map('unlink', glob($dir . '/*') ?: []);
array_map('unlink', glob($dir . '/.*epoch*') ?: []);
@rmdir($dir);

printf("%d checks, %d failures\n", $checks, $failures);
exit($failures === 0 ? 0 : 1);
