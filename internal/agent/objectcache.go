package agent

// APCuDropin is the Slipstream object-cache.php drop-in. For a single
// server, APCu (in-process shared memory) is faster and lighter than Redis:
// no daemon, no socket hop, no serialization over the wire. It implements
// WordPress's full WP_Object_Cache contract — global groups, non-persistent
// groups, multisite blog prefixing, incr/decr, and get_multiple.
//
// The canonical copy lives in connector/object-cache-apcu.php; keep the two
// in sync via TestObjectCacheCopiesMatch.
const APCuDropin = `<?php
/**
 * Slipstream APCu Object Cache (drop-in).
 * Single-server persistent object cache using APCu shared memory.
 * Managed by Slipstream — do not edit.
 */

if (!defined('ABSPATH')) { exit; }

if (!function_exists('apcu_fetch') || !apcu_enabled()) {
    // APCu unavailable: fall back to the default non-persistent cache by
    // not defining the drop-in classes. WordPress uses its built-in cache.
    return;
}

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
    private $prefix = '';         // per-install namespace (from AUTH_KEY)
    private $blog_prefix = 0;
    private $multisite = false;
    private $global_groups = [];
    private $non_persistent = [];
    private $runtime = [];        // per-request cache + non-persistent groups
    public  $cache_hits = 0;
    public  $cache_misses = 0;

    public function __construct() {
        $this->multisite = function_exists('is_multisite') && is_multisite();
        $this->blog_prefix = $this->multisite ? (int) get_current_blog_id() : 0;
        $salt = defined('AUTH_KEY') ? AUTH_KEY : (defined('DB_NAME') ? DB_NAME : 'slip');
        $this->prefix = 'slipoc:' . substr(md5($salt), 0, 8) . ':';
    }

    public function add_global_groups($groups) { foreach ((array) $groups as $g) { $this->global_groups[$g] = true; } }
    public function add_non_persistent_groups($groups) { foreach ((array) $groups as $g) { $this->non_persistent[$g] = true; } }
    public function switch_to_blog($blog_id) { $this->blog_prefix = $this->multisite ? (int) $blog_id : 0; }

    private function full_key($key, $group) {
        if (empty($group)) { $group = 'default'; }
        $blog = isset($this->global_groups[$group]) ? 0 : $this->blog_prefix;
        return $this->prefix . $blog . ':' . $group . ':' . $key;
    }

    public function add($key, $data, $group = 'default', $expire = 0) {
        if (wp_suspend_cache_addition()) { return false; }
        $id = $this->full_key($key, $group);
        if (isset($this->runtime[$id])) { return false; }
        return $this->set($key, $data, $group, $expire);
    }

    public function replace($key, $data, $group = 'default', $expire = 0) {
        if ($this->get($key, $group) === false) { return false; }
        return $this->set($key, $data, $group, $expire);
    }

    public function set($key, $data, $group = 'default', $expire = 0) {
        $id = $this->full_key($key, $group);
        if (is_object($data)) { $data = clone $data; }
        $this->runtime[$id] = $data;
        if (isset($this->non_persistent[$group])) { return true; }
        return apcu_store($id, $data, max(0, (int) $expire));
    }

    public function get($key, $group = 'default', $force = false, &$found = null) {
        $id = $this->full_key($key, $group);
        if (!$force && array_key_exists($id, $this->runtime)) {
            $found = true; $this->cache_hits++;
            $v = $this->runtime[$id];
            return is_object($v) ? clone $v : $v;
        }
        if (isset($this->non_persistent[$group])) { $found = false; $this->cache_misses++; return false; }
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
        if (isset($this->non_persistent[$group])) { return true; }
        return apcu_delete($id);
    }

    public function incr($key, $offset = 1, $group = 'default') {
        $id = $this->full_key($key, $group);
        $ok = false; $v = apcu_fetch($id, $ok);
        if (!$ok) { return false; }
        $v = max(0, (int) $v + (int) $offset);
        apcu_store($id, $v);
        $this->runtime[$id] = $v;
        return $v;
    }
    public function decr($key, $offset = 1, $group = 'default') { return $this->incr($key, -abs((int) $offset), $group); }

    public function flush() { $this->runtime = []; return apcu_clear_cache(); }
    public function flush_runtime() { $this->runtime = []; return true; }
    public function flush_group($group) {
        $prefix = $this->full_key('', $group);
        foreach (new APCUIterator('/^' . preg_quote($prefix, '/') . '/') as $item) { apcu_delete($item['key']); }
        foreach (array_keys($this->runtime) as $k) { if (strpos($k, $prefix) === 0) { unset($this->runtime[$k]); } }
        return true;
    }

    public function stats() {
        return ['hits' => $this->cache_hits, 'misses' => $this->cache_misses];
    }
}
`
