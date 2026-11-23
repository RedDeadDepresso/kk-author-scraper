package main

// Finds pixiv accounts that are mentioned on the forum kknow.cc (a XenForo forum) the way
// kk-mod-scraper looks for mod links there: a fresh forum search for "https://www.pixiv.net/users",
// newest posts first, until the post the previous run started from. Every new post is opened
// (search results only hold a truncated snippet) and the pixiv account ids in it are stored in
// cache/pixiv/kknow.json.
//
// These accounts have no koikatsucards author, so they don't go into the mapping's authors.
// They are added to the accounts whose profile is read (profile_links.go), which puts their
// description and links in cache/pixiv/profiles.json, the file kk-mod-scraper downloads to find
// mod links. Once the profile is read, an account with a name gets an entry in the authors
// list (addKknowAuthors), so it becomes a row of data/authors.json like any other authors.json
// entry, with the pixiv link and the pawchive, bepis, download and telegram matches the other
// rows get (matched by pixiv id, fanbox / patreon links on the profile, and the profile name).
//
// If the forum wants a login for searching, put the Cookie header value of a logged in browser
// session in the KKNOW_COOKIE environment variable (it is only sent to kknow.cc).
//
// There is no HTML parser in the standard library, so the pages are read with regular
// expressions that only need the stable XenForo 2 class names and attributes.

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	kknowSearchURL   = "https://kknow.cc/search/?q=https%3A%2F%2Fwww.pixiv.net%2Fusers&o=date"
	kknowUserAgent   = "kk-author-scraper-kknow/1.0"
	kknowCookieEnv   = "KKNOW_COOKIE"
	kknowCookieHost  = "kknow.cc"
	kknowMaxBodySize = 16 << 20
)

// kknowMarker identifies the newest forum post that was read.
type kknowMarker struct {
	ID   string `json:"id"`   // "post-93888", or "thread-3027" for a result that is a thread's first post
	URL  string `json:"url"`  // for humans
	Time string `json:"time"` // RFC 3339, UTC
}

// kknowUser is a pixiv account found on the forum.
type kknowUser struct {
	Post    string `json:"post"` // the post it was first seen in
	FoundAt string `json:"found_at"`
}

// KknowCache is cache/pixiv/kknow.json.
type KknowCache struct {
	NewestPost *kknowMarker         `json:"newest_post,omitempty"`
	Users      map[string]kknowUser `json:"users"`
}

// ---------------------------------------------------------------------------
// Reading the pages
// ---------------------------------------------------------------------------

type kknowHit struct {
	Key     string // "post-93888", or "thread-3027" when the row points at a thread
	URL     string
	Time    time.Time
	Snippet string
}

var (
	kknowPostRe     = regexp.MustCompile(`/post-(\d+)|/posts/(\d+)`)
	kknowThreadRe   = regexp.MustCompile(`/threads/[^/?#]*?\.?(\d+)(?:[/?#]|$)`)
	kknowRowRe      = regexp.MustCompile(`<li[^>]*class="[^"]*\bblock-row\b[^"]*"[^>]*>`)
	kknowTitleRe    = regexp.MustCompile(`(?s)contentRow-title">(.*?)</h3>`)
	kknowHrefRe     = regexp.MustCompile(`<a[^>]+href="([^"]+)"`)
	kknowTimeRe     = regexp.MustCompile(`data-time="(\d+)"`)
	kknowSnippetRe  = regexp.MustCompile(`(?s)contentRow-snippet">(.*?)</div>`)
	kknowTagRe      = regexp.MustCompile(`<[^>]*>`)
	kknowNextRe     = regexp.MustCompile(`<link[^>]+rel="next"[^>]*href="([^"]+)"`)
	kknowNextRe2    = regexp.MustCompile(`<link[^>]+href="([^"]+)"[^>]*rel="next"`)
	kknowArticleRe  = regexp.MustCompile(`<article[^>]*data-content="(post-\d+)"`)
	kknowCSRFHtmlRe = regexp.MustCompile(`<html[^>]*data-csrf="([^"]+)"`)
	kknowCSRFFormRe = regexp.MustCompile(`name="_xfToken"[^>]*value="([^"]+)"`)
)

func kknowHitKey(href string) string {
	if m := kknowPostRe.FindStringSubmatch(href); m != nil {
		return "post-" + m[1] + m[2]
	}
	if m := kknowThreadRe.FindStringSubmatch(href); m != nil {
		return "thread-" + m[1]
	}
	return ""
}

// parseKknowHits reads the rows of a XenForo 2 search result page.
func parseKknowHits(page string, base *url.URL) []kknowHit {
	idx := kknowRowRe.FindAllStringIndex(page, -1)
	var hits []kknowHit
	for i, loc := range idx {
		end := len(page)
		if i+1 < len(idx) {
			end = idx[i+1][0]
		}
		row := page[loc[0]:end]
		title := kknowTitleRe.FindStringSubmatch(row)
		if title == nil {
			continue
		}
		links := kknowHrefRe.FindAllStringSubmatch(title[1], -1)
		if len(links) == 0 {
			continue
		}
		href := strings.TrimSpace(html.UnescapeString(links[len(links)-1][1]))
		key := kknowHitKey(href)
		if key == "" {
			log.Printf("kknow: skipping a search result with an unrecognised link %q", href)
			continue
		}
		ref, err := url.Parse(href)
		if err != nil {
			continue
		}
		hit := kknowHit{Key: key, URL: base.ResolveReference(ref).String()}
		if m := kknowSnippetRe.FindStringSubmatch(row); m != nil {
			hit.Snippet = html.UnescapeString(kknowTagRe.ReplaceAllString(m[1], " "))
		}
		if m := kknowTimeRe.FindStringSubmatch(row); m != nil {
			if secs, err := strconv.ParseInt(m[1], 10, 64); err == nil {
				hit.Time = time.Unix(secs, 0).UTC()
			}
		}
		hits = append(hits, hit)
	}
	return hits
}

func nextKknowPage(page string, base *url.URL) string {
	m := kknowNextRe.FindStringSubmatch(page)
	if m == nil {
		m = kknowNextRe2.FindStringSubmatch(page)
	}
	if m == nil {
		return ""
	}
	ref, err := url.Parse(strings.TrimSpace(html.UnescapeString(m[1])))
	if err != nil {
		return ""
	}
	return base.ResolveReference(ref).String()
}

// kknowPost is the HTML of one post of a thread page.
type kknowPost struct {
	Key  string // "post-93807"
	HTML string
}

// splitKknowPosts cuts a thread page into its posts, in page order.
func splitKknowPosts(page string) []kknowPost {
	idx := kknowArticleRe.FindAllStringSubmatchIndex(page, -1)
	posts := make([]kknowPost, 0, len(idx))
	for i, loc := range idx {
		end := len(page)
		if i+1 < len(idx) {
			end = idx[i+1][0]
		}
		posts = append(posts, kknowPost{Key: page[loc[2]:loc[3]], HTML: page[loc[0]:end]})
	}
	return posts
}

// kknowPixivUsers lists the pixiv account ids in a piece of HTML (links and text). It also
// looks at the text with HTML entities and percent-escapes decoded, which covers links in a
// redirect/proxy URL.
func kknowPixivUsers(src string) []string {
	texts := []string{src, html.UnescapeString(src)}
	if d, err := url.PathUnescape(texts[1]); err == nil {
		texts = append(texts, d)
	}
	seen := map[string]bool{}
	var ids []string
	for _, t := range texts {
		for _, m := range pixivUserRe.FindAllStringSubmatch(t, -1) {
			id := m[1] + m[2]
			if id != "" && !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	return ids
}

// ---------------------------------------------------------------------------
// HTTP
// ---------------------------------------------------------------------------

type kknowClient struct {
	http   *http.Client
	delay  time.Duration
	cookie string
	last   time.Time
}

func newKknowClient(delay time.Duration) *kknowClient {
	jar, _ := cookiejar.New(nil)
	return &kknowClient{
		http:   &http.Client{Timeout: 45 * time.Second, Jar: jar},
		delay:  delay,
		cookie: strings.TrimSpace(os.Getenv(kknowCookieEnv)),
	}
}

func (c *kknowClient) cookieFor(u *url.URL) string {
	h := strings.ToLower(u.Hostname())
	if c.cookie != "" && (h == kknowCookieHost || strings.HasSuffix(h, "."+kknowCookieHost)) {
		return c.cookie
	}
	return ""
}

// wait spaces the requests out, so the forum isn't hammered.
func (c *kknowClient) wait(ctx context.Context) error {
	if !c.last.IsZero() {
		if d := c.delay - time.Since(c.last); d > 0 {
			select {
			case <-time.After(d):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	c.last = time.Now()
	return nil
}

// do sends a request (GET, or a form POST) with a few retries on rate limits and outages and
// returns the page and its final URL (after redirects).
func (c *kknowClient) do(ctx context.Context, method, rawURL string, form url.Values) (string, *url.URL, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(time.Duration(attempt*attempt) * 2 * time.Second):
			case <-ctx.Done():
				return "", nil, ctx.Err()
			}
		}
		if err := c.wait(ctx); err != nil {
			return "", nil, err
		}
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
		if err != nil {
			return "", nil, err
		}
		req.Header.Set("User-Agent", kknowUserAgent)
		req.Header.Set("Accept", "text/html,application/xhtml+xml")
		req.Header.Set("Accept-Language", "en")
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		if ck := c.cookieFor(req.URL); ck != "" {
			req.Header.Set("Cookie", ck)
		}
		res, err := c.http.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return "", nil, ctx.Err()
			}
			lastErr = err
			continue
		}
		data, rerr := io.ReadAll(io.LimitReader(res.Body, kknowMaxBodySize))
		res.Body.Close()
		if rerr != nil {
			lastErr = rerr
			continue
		}
		if res.StatusCode == http.StatusTooManyRequests || res.StatusCode >= 500 {
			lastErr = fmt.Errorf("HTTP %d from %s", res.StatusCode, req.URL.Host)
			continue
		}
		if res.StatusCode != http.StatusOK {
			hint := ""
			if res.StatusCode == http.StatusForbidden {
				hint = " (blocked, or a login is needed: see " + kknowCookieEnv + ")"
			}
			return "", nil, fmt.Errorf("HTTP %d for %s%s", res.StatusCode, req.URL.Redacted(), hint)
		}
		return string(data), res.Request.URL, nil
	}
	return "", nil, fmt.Errorf("giving up on %s: %w", rawURL, lastErr)
}

// parseKknowSearchURL takes the site, the query and the ordering out of a search URL as the
// forum shows it (".../search/4687554/?q=https%3A%2F%2Fwww.pixiv.net%2Fusers&o=date").
func parseKknowSearchURL(raw string) (site *url.URL, query, order string, err error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, "", "", fmt.Errorf("not a valid search URL: %q", raw)
	}
	query = strings.TrimSpace(u.Query().Get("q"))
	if query == "" {
		return nil, "", "", fmt.Errorf("the search URL has no q= parameter: %q", raw)
	}
	order = u.Query().Get("o")
	if order == "" {
		order = "date"
	}
	return &url.URL{Scheme: u.Scheme, Host: u.Host}, query, order, nil
}

// search runs a new forum search and returns its first result page. The "/search/<id>/" page
// of the URL isn't reused: it belongs to the session that made it and expires. This is what the
// forum's own search form does: fetch a CSRF token, POST the keywords, follow the redirect.
func (c *kknowClient) search(ctx context.Context, site *url.URL, query, order string) (string, *url.URL, error) {
	formPage, _, err := c.do(ctx, http.MethodGet, site.String()+"/search/", nil)
	if err != nil {
		return "", nil, fmt.Errorf("could not open the search form: %w", err)
	}
	token := ""
	if m := kknowCSRFHtmlRe.FindStringSubmatch(formPage); m != nil {
		token = m[1]
	} else if m := kknowCSRFFormRe.FindStringSubmatch(formPage); m != nil {
		token = m[1]
	}
	form := url.Values{"keywords": {query}, "order": {order}}
	if token != "" {
		form.Set("_xfToken", html.UnescapeString(token))
	}
	page, final, err := c.do(ctx, http.MethodPost, site.String()+"/search/search", form)
	if err != nil {
		return "", nil, fmt.Errorf("the search failed: %w", err)
	}
	return page, final, nil
}

// ---------------------------------------------------------------------------
// Scraping
// ---------------------------------------------------------------------------

type kknowOptions struct {
	SearchURL string
	MaxPages  int
	Delay     time.Duration
	Full      bool // read every result again, not only those newer than the previous run
}

func kknowReached(m *kknowMarker, hit kknowHit) bool {
	if m == nil {
		return false
	}
	if hit.Key == m.ID {
		return true
	}
	if t, err := time.Parse(time.RFC3339, m.Time); err == nil && !hit.Time.IsZero() && hit.Time.Before(t) {
		return true
	}
	return false
}

// runKknow adds the pixiv accounts of the new forum posts to the cache. On an error the
// accounts found so far are kept, but newest_post doesn't move, so the posts that could not be
// read are looked at again next run.
func (a *App) runKknow(ctx context.Context, o kknowOptions) error {
	if a.kknow == nil {
		a.kknow = &KknowCache{}
	}
	if a.kknow.Users == nil {
		a.kknow.Users = map[string]kknowUser{}
	}
	site, query, order, err := parseKknowSearchURL(o.SearchURL)
	if err != nil {
		return err
	}
	c := newKknowClient(o.Delay)
	log.Printf("kknow: searching %s for %q", site.Host, query)
	page, pageURL, err := c.search(ctx, site, query, order)
	if err != nil {
		return err
	}
	marker := a.kknow.NewestPost
	if o.Full {
		marker = nil
	}

	var (
		fresh   []kknowHit
		newest  *kknowHit
		reached bool
		seen    = map[string]bool{}
	)
	for pageNo := 1; ; pageNo++ {
		hits := parseKknowHits(page, pageURL)
		if pageNo == 1 && len(hits) == 0 {
			return errors.New("the search returned no results (a login may be needed, or the forum's layout changed)")
		}
		for i := range hits {
			h := hits[i]
			if newest == nil {
				newest = &hits[i]
			}
			if kknowReached(marker, h) {
				reached = true
				break
			}
			if !seen[h.Key] {
				seen[h.Key] = true
				fresh = append(fresh, h)
			}
		}
		if reached {
			break
		}
		next := nextKknowPage(page, pageURL)
		if next == "" {
			break
		}
		if pageNo >= o.MaxPages {
			log.Printf("kknow: page limit reached (%d); older posts are not looked at", o.MaxPages)
			break
		}
		if page, pageURL, err = c.do(ctx, http.MethodGet, next, nil); err != nil {
			return fmt.Errorf("could not load result page %d: %w", pageNo+1, err)
		}
	}
	log.Printf("kknow: %d new posts to read (reached the previous run's post: %v)", len(fresh), reached)

	posts := map[string]string{} // post key -> HTML, for every post of every page opened
	postHTML := func(h kknowHit) (string, error) {
		if s, ok := posts[h.Key]; ok {
			return s, nil
		}
		body, _, err := c.do(ctx, http.MethodGet, h.URL, nil)
		if err != nil {
			return "", err
		}
		list := splitKknowPosts(body)
		for _, p := range list { // the page holds more posts; later hits may need them
			posts[p.Key] = p.HTML
		}
		if s, ok := posts[h.Key]; ok {
			return s, nil
		}
		if strings.HasPrefix(h.Key, "thread-") && len(list) > 0 {
			posts[h.Key] = list[0].HTML // a thread result stands for its first post
			return list[0].HTML, nil
		}
		log.Printf("kknow: post %s not found on its page; using the search snippet", h.Key)
		return h.Snippet, nil
	}

	added, failed := 0, 0
	for i := len(fresh) - 1; i >= 0; i-- { // oldest first
		if ctx.Err() != nil {
			return ctx.Err()
		}
		h := fresh[i]
		src, err := postHTML(h)
		if err != nil {
			log.Printf("kknow: could not read %s (%s): %v", h.Key, h.URL, err)
			failed++
			continue
		}
		for _, id := range kknowPixivUsers(src) {
			if _, ok := a.kknow.Users[id]; ok {
				continue
			}
			a.kknow.Users[id] = kknowUser{Post: h.URL, FoundAt: time.Now().UTC().Format(time.RFC3339)}
			added++
		}
	}
	log.Printf("kknow: %d new pixiv accounts, %d known in total", added, len(a.kknow.Users))

	if failed > 0 {
		return fmt.Errorf("%d of %d posts could not be read; they will be tried again next run", failed, len(fresh))
	}
	if newest != nil {
		a.kknow.NewestPost = &kknowMarker{ID: newest.Key, URL: newest.URL, Time: newest.Time.Format(time.RFC3339)}
	}
	return nil
}

// kknowAccounts lists the pixiv accounts found on the forum.
func (a *App) kknowAccounts() []string {
	if a.kknow == nil {
		return nil
	}
	ids := make([]string, 0, len(a.kknow.Users))
	for id := range a.kknow.Users {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// addKknowAuthors adds an authors.json entry (named after the pixiv profile) for every account
// found on kknow.cc whose profile was read and that has no entry yet, so that the authors file,
// pawchive, bepis and telegram matching treat it like the entries of the file. An account that
// is already in the file (by pixiv id) or that belongs to a koikatsucards author is skipped, and
// so is one whose profile is unavailable or has no name: it is tried again once its profile
// is read. Returns how many entries were added.
func (a *App) addKknowAuthors() int {
	ids := a.kknowAccounts()
	if len(ids) == 0 {
		return 0
	}
	have := map[string]bool{}
	for id := range a.mapping.PixivUsers {
		have[id] = true
	}
	if a.fileIdx != nil {
		for _, e := range a.fileIdx.entries {
			users, _ := pixivRefs(e.Links)
			for _, id := range users {
				have[id] = true
			}
		}
	}
	added := 0
	for _, id := range ids {
		if have[id] {
			continue
		}
		pe, ok := a.profileCache[id]
		name := strings.TrimSpace(pe.Name)
		if !ok || !pe.Available || name == "" {
			continue
		}
		if a.fileIdx == nil {
			a.fileIdx = newAuthorIndex()
		}
		a.fileIdx.add(&fileAuthor{Name: name, Links: []string{pixivBase + "/users/" + id}})
		a.fileIdx.count++
		have[id] = true
		added++
		if pe.Profile { // the links of the profile find the pawchive creator
			if a.mapping.PixivProfiles == nil {
				a.mapping.PixivProfiles = map[string]*PixivProfile{}
			}
			pp := &PixivProfile{Links: pe.Links, CheckedAt: pe.CheckedAt}
			if pp.Links == nil {
				pp.Links = []ProfileLink{}
			}
			a.mapping.PixivProfiles[id] = pp
		}
	}
	return added
}
