<?php
/**
 * Slipstream APCu Object Cache (drop-in).
 * Single-server persistent object cache using APCu shared memory.
 * Managed by Slipstream. Do not edit.
 */

if (!defined('ABSPATH')) { exit; }

/**
 * Is APCu actually usable in THIS process?
 *
 * This used to be a bare return at the top of the file, which did nothing at all. PHP hoists
 * unconditional top-level function and class declarations at compile time, so every
 * wp_cache_* function and WP_Object_Cache were declared before the guard ever ran. The
 * drop-in installed itself on hosts with no APCu, and on every wp-cli invocation, because
 * apc.enable_cli defaults to 0. Measured on a live box: apcu_enabled() false, and the live
 * $wp_object_cache still coming from this file.
 *
 * The consequences were not theoretical. apcu_add() returns false when the segment is not
 * initialised, so wp_cache_add() always failed, and WordPress uses it as a lock (doing_cron
 * is one). And "wp cache flush" fatalled outright: "APC must be enabled to use APCUIterator".
 *
 * So the check now sets a flag the class reads, and when APCu is unusable the class behaves
 * exactly like WordPress's own non-persistent cache: correct, just not shared.
 */
define('SLIPSTREAM_OC_APCU', (function () {
    if (!function_exists('apcu_fetch') || !apcu_enabled()) {
        return false;
    }
    // apcu_enabled() reports apc.enabled and says nothing about apc.enable_cli, which is
    // what actually decides whether the segment gets initialised in a CLI process.
    if (PHP_SAPI === 'cli' && !filter_var((string) ini_get('apc.enable_cli'), FILTER_VALIDATE_BOOLEAN)) {
        return false;
    }
    return true;
})());

function wp_cache_init() { $GLOBALS['wp_object_cache'] = new WP_Object_Cache(); }
function wp_cache_add($key, $data, $group = '', $expire = 0) { return $GLOBALS['wp_object_cache']->add($key, $data, $group, (int) $expire); }
function wp_cache_add_multiple(array $data, $group = '', $expire = 0) { $r = []; foreach ($data as $k => $v) { $r[$k] = wp_cache_add($k, $v, $group, $expire); } return $r; }
function wp_cache_replace($key, $data, $group = '', $expire = 0) { return $GLOBALS['wp_object_cache']->replace($key, $data, $group, (int) $expire); }
function wp_cache_set($key, $data, $group = '', $expire = 0) { return $GLOBALS['wp_object_cache']->set($key, $data, $group, (int) $expire); }
function wp_cache_set_multiple(array $data, $group = '', $expire = 0) { $r = []; foreach ($data as $k => $v) { $r[$k] = wp_cache_set($k, $v, $group, $expire); } return $r; }
function wp_cache_get($key, $group = '', $force = false, &$found = null) { return $GLOBALS['wp_object_cache']->get($key, $group, $force, $found); }
function wp_cache_get_multiple($keys, $group = '', $force = false) { return $GLOBALS['wp_object_cache']->get_multiple($keys, $group, $force); }
function wp_cache_delete($key, $group = '') { return $GLOBALS['wp_object_cache']->delete($key, $group); }
function wp_cache_delete_multiple(array $keys, $group = '') { $r = []; foreach ($keys as $k) { $r[$k] = wp_cache_delete($k, $group); } return $r; }
function wp_cache_incr($key, $offset = 1, $group = '') { return $GLOBALS['wp_object_cache']->incr($key, $offset, $group); }
function wp_cache_decr($key, $offset = 1, $group = '') { return $GLOBALS['wp_object_cache']->decr($key, $offset, $group); }
function wp_cache_flush() { return $GLOBALS['wp_object_cache']->flush(); }
function wp_cache_flush_runtime() { return $GLOBALS['wp_object_cache']->flush_runtime(); }
function wp_cache_flush_group($group) { return $GLOBALS['wp_object_cache']->flush_group($group); }
function wp_cache_supports($feature) { return in_array($feature, ['add_multiple','set_multiple','get_multiple','delete_multiple','flush_runtime','flush_group'], true); }
function wp_cache_close() { return true; }
function wp_cache_add_global_groups($groups) { $GLOBALS['wp_object_cache']->add_global_groups($groups); }
function wp_cache_add_non_persistent_groups($groups) { $GLOBALS['wp_object_cache']->add_non_persistent_groups($groups); }
function wp_cache_switch_to_blog($blog_id) { $GLOBALS['wp_object_cache']->switch_to_blog($blog_id); }

class WP_Object_Cache {
    private $prefix = '';         // per-install namespace (from AUTH_KEY) plus the epoch
    private $blog_prefix = 0;
    private $multisite = false;
    private $global_groups = [];
    private $non_persistent = [];
    private $runtime = [];        // per-request cache + non-persistent groups
    private $apcu = false;        // is the shared segment usable in this process
    private $site = '';
    public  $cache_hits = 0;
    public  $cache_misses = 0;

    /**
     * Where the epoch lives.
     *
     * APCu is shared memory owned by the PHP-FPM master, so a second process cannot reach
     * into it. wp-cli is a second process. That means "wp cache flush", and every option
     * wp-cli writes, left the web tier serving what it had cached, indefinitely, until
     * something restarted PHP-FPM. On a fresh site that showed up as the panel activating a
     * theme and the site continuing to serve the old one.
     *
     * A file both processes can read fixes it without a daemon. Every key carries an epoch
     * read from this file, so bumping the file renames the whole namespace and both
     * processes see the flush at once.
     *
     * The epoch is random, not a counter. If the file is lost, a deploy replacing a copied
     * drop-in is the usual way, a counter would restart at zero and start reading keys some
     * earlier generation wrote. A fresh random value can only ever miss.
     */
    private function epoch_file() {
        return __DIR__ . '/.slipstream-cache-epoch';
    }

    // Static, so the file is read once per process rather than once per instance. It has to
    // be a class property and not a static local: write_epoch() must be able to move it, or
    // an instance built after a flush in the same process reads the pre-flush value out of
    // the cached static and addresses the namespace we just abandoned.
    private static $epoch = null;

    private function epoch() {
        if (self::$epoch !== null) { return self::$epoch; }

        $raw = @file_get_contents($this->epoch_file());
        if (is_string($raw) && preg_match('/^[0-9a-f]{16}$/', trim($raw))) {
            return self::$epoch = trim($raw);
        }
        return $this->write_epoch();
    }

    private function write_epoch() {
        $new = bin2hex(function_exists('random_bytes') ? random_bytes(8) : pack('NN', mt_rand(), mt_rand()));
        $file = $this->epoch_file();
        // Write to a neighbour and rename, so a reader never sees half a value.
        $tmp = $file . '.' . getmypid();
        if (@file_put_contents($tmp, $new, LOCK_EX) !== false && @rename($tmp, $file)) {
            @chmod($file, 0644);
            return self::$epoch = $new;
        }
        @unlink($tmp);
        // Unwritable directory. Fall back to a fixed epoch so the cache still works; the
        // cross-process flush is what is lost, and flush() reports that honestly.
        return self::$epoch = '0000000000000000';
    }

    public function __construct() {
        $this->apcu = defined('SLIPSTREAM_OC_APCU') && SLIPSTREAM_OC_APCU;
        $this->multisite = function_exists('is_multisite') && is_multisite();
        $this->blog_prefix = $this->multisite ? (int) get_current_blog_id() : 0;
        $salt = defined('AUTH_KEY') ? AUTH_KEY : (defined('DB_NAME') ? DB_NAME : 'slip');
        $this->site = 'slipoc:' . substr(md5($salt), 0, 8) . ':';
        $this->prefix = $this->site . $this->epoch() . ':';
    }

    public function add_global_groups($groups) { foreach ((array) $groups as $g) { $this->global_groups[$g] = true; } }
    public function add_non_persistent_groups($groups) { foreach ((array) $groups as $g) { $this->non_persistent[$g] = true; } }
    public function switch_to_blog($blog_id) { $this->blog_prefix = $this->multisite ? (int) $blog_id : 0; }

    private function full_key($key, $group) {
        if (empty($group)) { $group = 'default'; }
        $blog = isset($this->global_groups[$group]) ? 0 : $this->blog_prefix;
        return $this->prefix . $blog . ':' . $group . ':' . $key;
    }

    // A group is held in the request only when WordPress said so, or when there is no
    // shared segment to hold it in.
    private function runtime_only($group) {
        return !$this->apcu || isset($this->non_persistent[$group]);
    }

    public function add($key, $data, $group = 'default', $expire = 0) {
        if (wp_suspend_cache_addition()) { return false; }
        $id = $this->full_key($key, $group);
        if (isset($this->runtime[$id])) { return false; }
        if ($this->runtime_only($group)) { $this->runtime[$id] = $data; return true; }
        // apcu_add is atomic set-if-absent, and WordPress relies on that for
        // locks (e.g. doing_cron); a plain store would let two workers both
        // "acquire" the lock.
        if (apcu_add($id, is_object($data) ? clone $data : $data, max(0, (int) $expire))) {
            $this->runtime[$id] = $data;
            return true;
        }
        return false;
    }

    public function replace($key, $data, $group = 'default', $expire = 0) {
        if ($this->get($key, $group) === false) { return false; }
        return $this->set($key, $data, $group, $expire);
    }

    public function set($key, $data, $group = 'default', $expire = 0) {
        $id = $this->full_key($key, $group);
        if (is_object($data)) { $data = clone $data; }
        $this->runtime[$id] = $data;
        if ($this->runtime_only($group)) { return true; }
        return apcu_store($id, $data, max(0, (int) $expire));
    }

    public function get($key, $group = 'default', $force = false, &$found = null) {
        $id = $this->full_key($key, $group);
        if (!$force && array_key_exists($id, $this->runtime)) {
            $found = true; $this->cache_hits++;
            $v = $this->runtime[$id];
            return is_object($v) ? clone $v : $v;
        }
        if ($this->runtime_only($group)) { $found = false; $this->cache_misses++; return false; }
        $ok = false;
        $v = apcu_fetch($id, $ok);
        if ($ok) {
            $this->runtime[$id] = $v; $found = true; $this->cache_hits++;
            return is_object($v) ? clone $v : $v;
        }
        $found = false; $this->cache_misses++;
        return false;
    }

    public function get_multiple($keys, $group = 'default', $force = false) {
        $out = [];
        foreach ($keys as $k) { $out[$k] = $this->get($k, $group, $force); }
        return $out;
    }

    public function delete($key, $group = 'default') {
        $id = $this->full_key($key, $group);
        unset($this->runtime[$id]);
        if ($this->runtime_only($group)) { return true; }
        return apcu_delete($id);
    }

    public function incr($key, $offset = 1, $group = 'default') {
        $id = $this->full_key($key, $group);
        if ($this->runtime_only($group)) {
            if (!array_key_exists($id, $this->runtime)) { return false; }
            return $this->runtime[$id] = max(0, (int) $this->runtime[$id] + (int) $offset);
        }
        $ok = false; $v = apcu_fetch($id, $ok);
        if (!$ok) { return false; }
        $v = max(0, (int) $v + (int) $offset);
        apcu_store($id, $v);
        $this->runtime[$id] = $v;
        return $v;
    }
    public function decr($key, $offset = 1, $group = 'default') { return $this->incr($key, -abs((int) $offset), $group); }

    public function flush() {
        $this->runtime = [];

        // Bumping the epoch is what makes this work across processes: PHP-FPM reads the same
        // file and starts computing different keys on its very next request. Do it first,
        // because it is the part that must not be skipped.
        $new = $this->write_epoch();
        $moved = ($new !== '0000000000000000');
        if ($moved) {
            $this->prefix = $this->site . $new . ':';
        }

        // Then clear the keys we just orphaned, so a busy site does not carry two
        // generations in the segment until they evict. Best effort: APCUIterator throws
        // when the segment is not initialised, which is every CLI process.
        if ($this->apcu && class_exists('APCUIterator')) {
            try {
                foreach (new APCUIterator('/^' . preg_quote($this->site, '/') . '/') as $item) {
                    apcu_delete($item['key']);
                }
                return true;
            } catch (Throwable $e) {
                // fall through to the epoch result
            }
        }
        return $moved;
    }

    public function flush_runtime() { $this->runtime = []; return true; }

    public function flush_group($group) {
        $prefix = $this->full_key('', $group);
        foreach (array_keys($this->runtime) as $k) { if (strpos($k, $prefix) === 0) { unset($this->runtime[$k]); } }
        if (!$this->apcu || !class_exists('APCUIterator')) { return true; }
        try {
            foreach (new APCUIterator('/^' . preg_quote($prefix, '/') . '/') as $item) { apcu_delete($item['key']); }
        } catch (Throwable $e) {
            return false;
        }
        return true;
    }

    public function stats() {
        return ['hits' => $this->cache_hits, 'misses' => $this->cache_misses];
    }
}
