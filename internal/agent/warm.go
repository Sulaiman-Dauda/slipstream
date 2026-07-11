package agent

import (
	"context"
	"crypto/tls"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/slipstream-panel/slipstream/internal/rpc"
	"github.com/slipstream-panel/slipstream/internal/state"
)

// Cache warming: after a deploy or purge, the first visitor to each page
// pays the full cold-render cost (~130ms+ on a small box) while the origin
// regenerates it. Warming crawls the sitemap and requests each URL over
// loopback so the full-page cache is already hot when real traffic arrives.
// CloudPanel does not do this automatically — it is a differentiator.

var locRe = regexp.MustCompile(`<loc>\s*([^<\s]+)\s*</loc>`)

func warmClient() *http.Client {
	return &http.Client{
		Timeout: 20 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig:   &tls.Config{InsecureSkipVerify: true},
			DisableKeepAlives: false,
			MaxIdleConns:      4,
		},
		// Do not follow redirects into other hosts; we only warm this site.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// WarmCache fetches the site's sitemap and warms each URL. Requests go to
// 127.0.0.1 with the site's Host header so they hit this server's nginx
// cache regardless of DNS.
func (a *Agent) WarmCache(p rpc.WarmParams) (rpc.WarmResult, error) {
	if err := validateSite(p.Site); err != nil {
		return rpc.WarmResult{}, err
	}
	maxURLs := p.MaxURLs
	if maxURLs <= 0 || maxURLs > 500 {
		maxURLs = 200
	}
	client := warmClient()
	host := p.Site.Domain

	// For WordPress, ask wp-cli for the published URL list directly — far
	// more reliable than crawling a sitemap through nginx's rewrite. Fall
	// back to sitemap discovery for other site types.
	var urls []string
	if p.Site.Type == state.SiteWordPress || p.Site.Type == state.SiteWooCommerce {
		urls = a.wordpressURLs(p.Site, maxURLs)
	}
	if len(urls) == 0 {
		urls = a.collectSitemapURLs(client, host, maxURLs)
	}
	// Always include the homepage.
	urls = append([]string{"https://" + host + "/"}, urls...)

	var res rpc.WarmResult
	for _, u := range urls {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://127.0.0.1"+pathOf(u, host), nil)
		if err != nil {
			continue
		}
		req.Host = host
		req.Header.Set("User-Agent", "Slipstream-CacheWarmer/1.0")
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		io.Copy(io.Discard, io.LimitReader(resp.Body, 2<<20))
		cacheHdr := resp.Header.Get("X-Slipstream-Cache")
		resp.Body.Close()
		res.Warmed++
		// A second request confirms it is now cached.
		if req2, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://127.0.0.1"+pathOf(u, host), nil); err == nil {
			req2.Host = host
			if resp2, err := client.Do(req2); err == nil {
				io.Copy(io.Discard, io.LimitReader(resp2.Body, 2<<20))
				if resp2.Header.Get("X-Slipstream-Cache") == "HIT" || cacheHdr == "HIT" {
					res.Cached++
				}
				resp2.Body.Close()
			}
		}
	}
	return res, nil
}

// wordpressURLs asks wp-cli for published post and page permalinks.
func (a *Agent) wordpressURLs(site state.Site, max int) []string {
	ctx := context.Background()
	out, err := a.wp(ctx, site, "post", "list",
		"--post_type=post,page", "--post_status=publish", "--field=url", "--posts_per_page="+itoaSmall(max))
	if err != nil {
		return nil
	}
	var urls []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		u := strings.TrimSpace(line)
		if strings.HasPrefix(u, "http") {
			urls = append(urls, u)
			if len(urls) >= max {
				break
			}
		}
	}
	return urls
}

func itoaSmall(n int) string {
	if n <= 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func (a *Agent) collectSitemapURLs(client *http.Client, host string, max int) []string {
	seen := map[string]bool{}
	var out []string
	// WordPress core (pretty and plain-permalink forms) plus common plugins.
	for _, sm := range []string{"/wp-sitemap.xml", "/?sitemap=index", "/sitemap.xml", "/sitemap_index.xml"} {
		body := a.fetch(client, host, sm)
		if body == "" {
			continue
		}
		for _, m := range locRe.FindAllStringSubmatch(body, -1) {
			loc := decodeEntities(m[1])
			// A <loc> that is itself a sitemap (index → sub-sitemaps) is
			// recursed one level; anything else is a real page.
			if isSitemapURL(loc) {
				sub := a.fetch(client, host, pathOf(loc, host))
				for _, sm2 := range locRe.FindAllStringSubmatch(sub, -1) {
					if addURL(seen, &out, decodeEntities(sm2[1]), max) {
						return out
					}
				}
				continue
			}
			if addURL(seen, &out, loc, max) {
				return out
			}
		}
		if len(out) > 0 {
			break
		}
	}
	return out
}

func isSitemapURL(loc string) bool {
	return strings.Contains(loc, "sitemap") && (strings.HasSuffix(loc, ".xml") || strings.Contains(loc, "sitemap="))
}

func decodeEntities(s string) string {
	return strings.NewReplacer("&amp;", "&", "&#038;", "&", "&lt;", "<", "&gt;", ">").Replace(s)
}

func addURL(seen map[string]bool, out *[]string, u string, max int) bool {
	if !seen[u] {
		seen[u] = true
		*out = append(*out, u)
	}
	return len(*out) >= max
}

func (a *Agent) fetch(client *http.Client, host, path string) string {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://127.0.0.1"+path, nil)
	if err != nil {
		return ""
	}
	req.Host = host
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return ""
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return string(b)
}

// pathOf returns the path+query of a URL for the given host, defaulting to
// "/" when parsing is not worthwhile.
func pathOf(rawURL, host string) string {
	// Strip scheme://host prefix if present.
	if i := indexHost(rawURL, host); i >= 0 {
		p := rawURL[i+len(host):]
		if p == "" {
			return "/"
		}
		return p
	}
	// Already a path.
	if len(rawURL) > 0 && rawURL[0] == '/' {
		return rawURL
	}
	return "/"
}

func indexHost(s, host string) int {
	for i := 0; i+len(host) <= len(s); i++ {
		if s[i:i+len(host)] == host {
			return i
		}
	}
	return -1
}
