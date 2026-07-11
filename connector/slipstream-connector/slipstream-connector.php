<?php
/**
 * Plugin Name: Slipstream Connector
 * Description: Precise cache invalidation and performance metrics for the Slipstream panel.
 * Version: 1.0.0
 * Author: Slipstream
 */

if (!defined('ABSPATH')) {
    exit;
}

final class Slipstream_Connector
{
    public static function boot(): void
    {
        add_action('init', [__CLASS__, 'handle_magic_login'], 1);
        add_action('transition_post_status', [__CLASS__, 'on_post_change'], 10, 3);
        add_action('comment_post', [__CLASS__, 'on_comment'], 10, 2);
        add_action('wp_set_comment_status', [__CLASS__, 'on_comment'], 10, 1);
        add_action('switch_theme', [__CLASS__, 'purge_all']);
        add_action('activated_plugin', [__CLASS__, 'purge_all']);
        add_action('deactivated_plugin', [__CLASS__, 'purge_all']);
        add_action('wp_update_nav_menu', [__CLASS__, 'purge_all']);
        add_action('customize_save_after', [__CLASS__, 'purge_all']);
        add_action('wp_footer', [__CLASS__, 'metrics_comment'], PHP_INT_MAX);

        // WooCommerce cart fragments fire an admin-ajax request on EVERY
        // anonymous page, punching through the full-page cache. Dequeue it
        // when the panel enables commerce mode; the cart still works, it
        // just updates on navigation rather than via a per-page AJAX call.
        if (defined('SLIPSTREAM_DISABLE_CART_FRAGMENTS') && SLIPSTREAM_DISABLE_CART_FRAGMENTS) {
            add_action('wp_enqueue_scripts', [__CLASS__, 'kill_cart_fragments'], 11);
        }
    }

    public static function kill_cart_fragments(): void
    {
        if (is_cart() || is_checkout()) { return; }
        wp_dequeue_script('wc-cart-fragments');
    }

    /**
     * One-click login from the Slipstream panel. The panel stores a
     * sha256(token):expiry in an admin's user meta; we read it with a DIRECT
     * database query (bypassing the object cache) so it works under APCu,
     * Redis, or no cache, then set the auth cookie and redirect to wp-admin.
     */
    public static function handle_magic_login(): void
    {
        if (empty($_GET['slipstream_login'])) {
            return;
        }
        global $wpdb;
        $token = (string) $_GET['slipstream_login'];
        if (!preg_match('/^[a-f0-9]{48}$/', $token)) {
            wp_die('Invalid login link.');
        }
        $hash = hash('sha256', $token);
        $row = $wpdb->get_row($wpdb->prepare(
            "SELECT user_id, meta_value FROM {$wpdb->usermeta} WHERE meta_key = 'slipstream_magic' LIMIT 1"
        ));
        if (!$row) {
            wp_die('This login link has expired. Generate a new one from the panel.');
        }
        list($stored, $expiry) = array_pad(explode(':', (string) $row->meta_value, 2), 2, '0');
        delete_user_meta((int) $row->user_id, 'slipstream_magic');
        if (!hash_equals($stored, $hash) || time() > (int) $expiry) {
            wp_die('This login link has expired. Generate a new one from the panel.');
        }
        wp_set_auth_cookie((int) $row->user_id, false, true);
        wp_safe_redirect(admin_url());
        exit;
    }

    /** Purge exactly the URLs affected by a post changing, not the site. */
    public static function on_post_change(string $new_status, string $old_status, WP_Post $post): void
    {
        if ($new_status !== 'publish' && $old_status !== 'publish') {
            return;
        }
        if (wp_is_post_revision($post) || wp_is_post_autosave($post)) {
            return;
        }

        $urls = [
            get_permalink($post),
            home_url('/'),
            get_post_type_archive_link($post->post_type) ?: null,
            get_bloginfo('rss2_url'),
        ];

        foreach ((array) get_the_category($post->ID) as $cat) {
            $urls[] = get_category_link($cat);
        }
        foreach ((array) get_the_tags($post->ID) ?: [] as $tag) {
            $urls[] = get_tag_link($tag);
        }
        $urls[] = get_author_posts_url((int) $post->post_author);
        $urls[] = get_year_link((int) get_the_date('Y', $post));
        $urls[] = get_month_link((int) get_the_date('Y', $post), (int) get_the_date('m', $post));

        self::purge(array_values(array_filter(array_unique($urls))));
    }

    public static function on_comment($comment_id, $approved = null): void
    {
        $comment = get_comment($comment_id);
        if ($comment && $comment->comment_post_ID) {
            self::purge([get_permalink((int) $comment->comment_post_ID)]);
        }
    }

    public static function purge_all(): void
    {
        self::request('/api/connector/purge', ['all' => true]);
    }

    private static function purge(array $urls): void
    {
        if ($urls === []) {
            return;
        }
        self::request('/api/connector/purge', ['urls' => $urls]);
    }

    private static function request(string $path, array $body): void
    {
        $endpoint = defined('SLIPSTREAM_ENDPOINT') ? SLIPSTREAM_ENDPOINT : 'http://127.0.0.1:9080';
        $token = defined('SLIPSTREAM_SITE_TOKEN') ? SLIPSTREAM_SITE_TOKEN : '';
        if ($token === '') {
            return;
        }
        wp_remote_post($endpoint . $path, [
            'timeout'  => 2,
            'blocking' => false,
            'headers'  => [
                'Content-Type'  => 'application/json',
                'Authorization' => 'Bearer ' . $token,
            ],
            'body' => wp_json_encode($body),
        ]);
    }

    /**
     * Performance Guard reads this footer comment to compare database load
     * between production and a candidate release.
     */
    public static function metrics_comment(): void
    {
        if (is_user_logged_in()) {
            return;
        }
        printf(
            "\n<!-- slipstream queries=%d time=%.4f mem=%d -->\n",
            (int) get_num_queries(),
            (float) timer_stop(0, 4),
            (int) memory_get_peak_usage(true)
        );
    }
}

Slipstream_Connector::boot();
