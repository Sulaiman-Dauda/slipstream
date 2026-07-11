package agent

// ConnectorPHP is the Slipstream WordPress mu-plugin, installed into every
// WordPress site. It gives Velocity Engine precise cache invalidation
// (purge exactly the URLs a content change affects, never the whole site)
// and emits per-request metrics for Performance Guard.
//
// The canonical copy lives in connector/slipstream-connector/; keep the two
// in sync via TestConnectorCopiesMatch.
const ConnectorPHP = `<?php
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
        add_action('transition_post_status', [__CLASS__, 'on_post_change'], 10, 3);
        add_action('comment_post', [__CLASS__, 'on_comment'], 10, 2);
        add_action('wp_set_comment_status', [__CLASS__, 'on_comment'], 10, 1);
        add_action('switch_theme', [__CLASS__, 'purge_all']);
        add_action('activated_plugin', [__CLASS__, 'purge_all']);
        add_action('deactivated_plugin', [__CLASS__, 'purge_all']);
        add_action('wp_update_nav_menu', [__CLASS__, 'purge_all']);
        add_action('customize_save_after', [__CLASS__, 'purge_all']);
        add_action('wp_footer', [__CLASS__, 'metrics_comment'], PHP_INT_MAX);
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
`
