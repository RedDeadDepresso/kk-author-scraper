package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	koikatsuBase = "https://koikatsucards.com"
	pixivBase    = "https://www.pixiv.net"
	userAgent    = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36"
)

// ---------------------------------------------------------------------------
// Output data structure (data/mapping.json)
// ---------------------------------------------------------------------------

// Post links one pixiv artwork to its koikatsucards page.
type Post struct {
	PixivID     string `json:"pixiv_id"`
	PixivURL    string `json:"pixiv_url"`
	KoikatsuURL string `json:"koikatsu_url"`
	Title       string `json:"title,omitempty"`
	AuthorName  string `json:"author_name,omitempty"`
	AuthorSlug  string `json:"author_slug,omitempty"`
	PixivUserID string `json:"pixiv_user_id,omitempty"`
	PawchiveURL string `json:"pawchive_url,omitempty"`
	WorkName    string `json:"work_name,omitempty"`
}

// Author is a koikatsucards author tag and the pixiv accounts seen for it.
type Author struct {
	Name          string         `json:"name"`
	Slug          string         `json:"slug"`
	KoikatsuURL   string         `json:"koikatsu_url"`
	PixivUserIDs  []string       `json:"pixiv_user_ids"`
	PixivUserURLs []string       `json:"pixiv_user_urls"`
	CreatorLinks  []string       `json:"creator_links,omitempty"` // fanbox/patreon links from authors.json
	Bepis         []BepisRef     `json:"bepis,omitempty"`
	Pawchive      *PawchiveMatch `json:"pawchive,omitempty"`
}

// PixivUser is a pixiv account and the koikatsucards author tags seen for it.
type PixivUser struct {
	ID                 string         `json:"id"`
	URL                string         `json:"url"`
	Name               string         `json:"name,omitempty"`
	AuthorSlugs        []string       `json:"author_slugs"`
	KoikatsuAuthorURLs []string       `json:"koikatsu_author_urls"`
	Sources            []string       `json:"sources,omitempty"` // "authors.json" and/or "pixiv_api"
	Pawchive           *PawchiveMatch `json:"pawchive,omitempty"`
}

// PixivProfile holds every link of a pixiv profile (description, homepage, social links).
// It exists for every pixiv account of the mapping and of the authors file, whether or not
// the account has a pawchive match.
type PixivProfile struct {
	Links     []ProfileLink `json:"links"`
	CheckedAt string        `json:"checked_at"`
	Error     string        `json:"error,omitempty"` // e.g. "not found" for a deleted account
}

// Mapping is the lookup structure. Every top-level map is keyed for O(1) access:
//
//	posts[pixivPostID]                -> Post (pixiv post  -> koikatsu post)
//	koikatsu_to_pixiv_post[koikatsuURL] -> pixiv URL (koikatsu post -> pixiv post)
//	pixiv_users[pixivUserID]          -> PixivUser (pixiv account -> koikatsu author)
//	authors[slug]                     -> Author (koikatsu author -> pixiv account)
//	author_url_to_slug / pixiv_user_url_to_id let you start from full URLs.
type Mapping struct {
	NewestPost       string                   `json:"newest_post"`
	UpdatedAt        string                   `json:"updated_at"`
	Posts            map[string]*Post         `json:"posts"`
	KoikatsuToPixiv  map[string]string        `json:"koikatsu_to_pixiv_post"`
	Authors          map[string]*Author       `json:"authors"`
	AuthorURLToSlug  map[string]string        `json:"author_url_to_slug"`
	PixivUsers       map[string]*PixivUser    `json:"pixiv_users"`
	PixivProfiles    map[string]*PixivProfile `json:"pixiv_profiles"`
	PixivUserURLToID map[string]string        `json:"pixiv_user_url_to_id"`
	PawchiveUsers    map[string]*PawchiveUser `json:"pawchive_users"`
	BepisAuthors     map[string]*BepisAuthor  `json:"bepis_authors"`
}

func (m *Mapping) init() {
	if m.Posts == nil {
		m.Posts = map[string]*Post{}
	}
	if m.KoikatsuToPixiv == nil {
		m.KoikatsuToPixiv = map[string]string{}
	}
	if m.Authors == nil {
		m.Authors = map[string]*Author{}
	}
	if m.AuthorURLToSlug == nil {
		m.AuthorURLToSlug = map[string]string{}
	}
	if m.PixivUsers == nil {
		m.PixivUsers = map[string]*PixivUser{}
	}
	if m.PixivProfiles == nil {
		m.PixivProfiles = map[string]*PixivProfile{}
	}
	if m.BepisAuthors == nil {
		m.BepisAuthors = map[string]*BepisAuthor{}
	}
	if m.PawchiveUsers == nil {
		m.PawchiveUsers = map[string]*PawchiveUser{}
	}
	if m.PixivUserURLToID == nil {
		m.PixivUserURLToID = map[string]string{}
	}
}

func addUnique(list []string, v string) []string {
	for _, e := range list {
		if e == v {
			return list
		}
	}
	list = append(list, v)
	sort.Strings(list)
	return list
}

func authorURL(slug string) string {
	return koikatsuBase + "/search?author=" + url.QueryEscape(slug)
}

// ---------------------------------------------------------------------------
// Caches (cache/*.json)
// ---------------------------------------------------------------------------

// TagEntry caches the result of /api/tags/search for an author or work name.
type TagEntry struct {
	Found     bool   `json:"found"`
	ID        int    `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
	Slug      string `json:"slug,omitempty"`
	CheckedAt string `json:"checked_at"`
}

// PixivEntry caches the result of the pixiv illust lookup.
type PixivEntry struct {
	Available   bool   `json:"available"`
	UserID      string `json:"user_id,omitempty"`
	UserName    string `json:"user_name,omitempty"`
	Error       string `json:"error,omitempty"`
	WithSession bool   `json:"with_session"` // unavailable entries are retried once a session is configured
	CheckedAt   string `json:"checked_at"`
}

// ---------------------------------------------------------------------------
// Helpers: files, env, HTTP
// ---------------------------------------------------------------------------

func loadEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		if _, set := os.LookupEnv(k); !set {
			os.Setenv(k, v)
		}
	}
}

func loadJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(b) == 0 {
		return nil
	}
	return json.Unmarshal(b, v)
}

func saveJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ") // Go sorts map keys, so diffs stay stable
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

type throttle struct {
	d    time.Duration
	last time.Time
}

func (t *throttle) wait(ctx context.Context) error {
	if wait := t.d - time.Since(t.last); wait > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
	t.last = time.Now()
	return nil
}

// ---------------------------------------------------------------------------
// App
// ---------------------------------------------------------------------------

type App struct {
	http        *http.Client
	session     string
	kThrottle   throttle
	pThrottle   throttle
	mapping     *Mapping
	authorCache map[string]TagEntry
	pixivCache  map[string]PixivEntry
	mappingPath string
	authorPath  string
	pixivPath   string
	kknowPath   string
	kknow       *KknowCache // pixiv accounts found on kknow.cc
	tgPixiv     *tgExport   // pixiv accounts and posts found in Telegram chats (tgsource.go)

	pawOverrides pawchiveOverrides // manual pawchive matches by authors.json row name

	rateLimitHits int
	oncePerAuthor bool
	lastHeader    http.Header // headers of the last successful response
	profilePath   string
	profileCache  map[string]ProfileEntry
	fileIdx       *authorIndex
	fileMemo      map[string]fileResult
	pawIdx        *creatorIndex // pawchive creators, set by runPawchive
	telegram      *telegramTags // channel hashtags, nil when not loaded
}

// get performs a GET with retries. Network errors and 5xx get short exponential
// backoff; HTTP 429 waits for Retry-After (or a long escalating delay) and is
// counted in rateLimitHits so callers can slow down.
func (a *App) get(ctx context.Context, rawURL string, headers map[string]string) (int, []byte, error) {
	const maxTransient, maxRateLimited = 4, 6
	var lastErr error
	transient, limited := 0, 0
	var wait time.Duration
	for {
		if wait > 0 {
			select {
			case <-ctx.Done():
				return 0, nil, ctx.Err()
			case <-time.After(wait):
			}
			wait = 0
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			return 0, nil, err
		}
		req.Header.Set("User-Agent", userAgent)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := a.http.Do(req)
		if err == nil {
			var body []byte
			body, err = io.ReadAll(resp.Body)
			resp.Body.Close()
			if err == nil {
				switch {
				case resp.StatusCode == http.StatusTooManyRequests:
					a.rateLimitHits++
					limited++
					lastErr = fmt.Errorf("HTTP 429 (rate limited) from %s", rawURL)
					if limited >= maxRateLimited {
						return 0, nil, lastErr
					}
					wait = time.Duration(30<<(limited-1)) * time.Second // 30s, 60s, 120s, ...
					if secs, perr := strconv.Atoi(strings.TrimSpace(resp.Header.Get("Retry-After"))); perr == nil && secs > 0 {
						if d := time.Duration(secs) * time.Second; d > wait {
							wait = d
						}
					}
					log.Printf("rate limited by %s; waiting %s (%d/%d)", resp.Request.URL.Host, wait, limited, maxRateLimited)
					continue
				case resp.StatusCode >= 500:
					lastErr = fmt.Errorf("HTTP %d from %s", resp.StatusCode, rawURL)
				default:
					a.lastHeader = resp.Header
					return resp.StatusCode, body, nil
				}
			}
		} else if ctx.Err() != nil {
			return 0, nil, ctx.Err()
		}
		if err != nil {
			lastErr = err
		}
		transient++
		if transient >= maxTransient {
			return 0, nil, lastErr
		}
		wait = time.Duration(1<<transient) * time.Second
	}
}

func (a *App) saveAll() error {
	a.mapping.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	return errors.Join(
		saveJSON(a.mappingPath, a.mapping),
		saveJSON(a.authorPath, a.authorCache),
		saveJSON(a.pixivPath, a.pixivCache),
		saveJSON(a.profilePath, a.profileCache),
		a.saveKknow(),
	)
}

// ---------------------------------------------------------------------------
// koikatsucards: listing pages
// ---------------------------------------------------------------------------

type Card struct {
	Work    string
	PixivID string
	Title   string
	Author  string
}

var (
	contentHrefRe = regexp.MustCompile(`href="/contents/([^"]+)"`)
	pixivSlugRe   = regexp.MustCompile(`^pixiv-(\d+)$`)
	nextLinkRe    = regexp.MustCompile(`<a[^>]*aria-disabled="(true|false)"[^>]*>\s*Next\s*</a`)
	eyebrowRe     = regexp.MustCompile(`<div class="eyebrow">([^<]*)</div>`)
	mutedRe       = regexp.MustCompile(`<p class="muted">([^<]*)</p>`)
	titleRe       = regexp.MustCompile(`<h3>([^<]*)</h3>`)
)

// pageResult is one parsed listing page.
type pageResult struct {
	Cards     []Card   // pixiv posts
	Links     int      // all /contents/ links on the page, pixiv or not
	Other     []string // slugs of non-pixiv links (not indexed)
	NextKnown bool     // the page has a "Next" pagination link
	HasNext   bool     // ... and it is enabled
}

// parsePage splits the page at each content link; the first <h3> and
// <p class="muted"> after a link belong to that card. Only /contents/pixiv-<digits>
// links become cards, but every content link counts toward "the page has results".
func parsePage(page string) pageResult {
	var res pageResult
	locs := contentHrefRe.FindAllStringSubmatchIndex(page, -1)
	seen := map[string]bool{}
	for i, loc := range locs {
		end := len(page)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		seg := page[loc[0]:end]
		slug := page[loc[2]:loc[3]]
		if seen[slug] {
			continue
		}
		seen[slug] = true
		res.Links++
		m := pixivSlugRe.FindStringSubmatch(slug)
		if m == nil {
			res.Other = append(res.Other, slug)
			continue
		}
		c := Card{PixivID: m[1]}
		if mm := mutedRe.FindStringSubmatch(seg); mm != nil {
			c.Author = strings.TrimSpace(html.UnescapeString(mm[1]))
		}
		if mm := titleRe.FindStringSubmatch(seg); mm != nil {
			c.Title = strings.TrimSpace(html.UnescapeString(mm[1]))
		}
		if mm := eyebrowRe.FindStringSubmatch(seg); mm != nil {
			c.Work = strings.TrimSpace(html.UnescapeString(mm[1]))
		}
		res.Cards = append(res.Cards, c)
	}
	if m := nextLinkRe.FindStringSubmatch(page); m != nil {
		res.NextKnown, res.HasNext = true, m[1] == "false"
	}
	return res
}

func (a *App) fetchPage(ctx context.Context, page int) (pageResult, error) {
	if err := a.kThrottle.wait(ctx); err != nil {
		return pageResult{}, err
	}
	status, body, err := a.get(ctx, fmt.Sprintf("%s/search?page=%d", koikatsuBase, page), nil)
	if err != nil {
		return pageResult{}, err
	}
	if status == http.StatusNotFound {
		return pageResult{}, nil
	}
	if status != http.StatusOK {
		return pageResult{}, fmt.Errorf("search page %d: HTTP %d", page, status)
	}
	return parsePage(string(body)), nil
}

// ---------------------------------------------------------------------------
// koikatsucards: author tag lookup
// ---------------------------------------------------------------------------

func (a *App) lookupAuthor(ctx context.Context, name string) (TagEntry, error) {
	return a.lookupTag(ctx, "AUTHOR", name, 1, a.authorCache)
}

// lookupTag resolves a tag name through /api/tags/search, caching the answer
// (including "not found"). Only a normalized exact name match is accepted.
func (a *App) lookupTag(ctx context.Context, typ, name string, limit int, cache map[string]TagEntry) (TagEntry, error) {
	key := strings.ToLower(name)
	if e, ok := cache[key]; ok {
		return e, nil
	}
	if err := a.kThrottle.wait(ctx); err != nil {
		return TagEntry{}, err
	}
	u := fmt.Sprintf("%s/api/tags/search?type=%s&limit=%d&q=%s", koikatsuBase, typ, limit, url.QueryEscape(name))
	status, body, err := a.get(ctx, u, nil)
	if err != nil {
		return TagEntry{}, err
	}
	if status != http.StatusOK {
		return TagEntry{}, fmt.Errorf("%s lookup %q: HTTP %d", strings.ToLower(typ), name, status)
	}
	var r struct {
		Items []struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
			Slug string `json:"slug"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return TagEntry{}, fmt.Errorf("%s lookup %q: %w", strings.ToLower(typ), name, err)
	}
	e := TagEntry{CheckedAt: time.Now().UTC().Format(time.RFC3339)}
	want := normName(name)
	for _, it := range r.Items {
		if strings.EqualFold(it.Name, name) || (want != "" && normName(it.Name) == want) {
			e.Found, e.ID, e.Name, e.Slug = true, it.ID, it.Name, it.Slug
			break
		}
	}
	if !e.Found && len(r.Items) > 0 {
		log.Printf("%s %q: top hit %q is not an exact match, skipping", strings.ToLower(typ), name, r.Items[0].Name)
	}
	cache[key] = e
	return e, nil
}

// ---------------------------------------------------------------------------
// pixiv: post -> account
// ---------------------------------------------------------------------------

func (a *App) lookupPixiv(ctx context.Context, id string) (PixivEntry, error) {
	if e, ok := a.pixivCache[id]; ok {
		// Failed lookups made without a session are worth retrying once one exists.
		if e.Available || e.WithSession || a.session == "" {
			return e, nil
		}
	}
	if err := a.pThrottle.wait(ctx); err != nil {
		return PixivEntry{}, err
	}
	headers := map[string]string{
		"Referer": pixivBase + "/",
		"Accept":  "application/json",
	}
	if a.session != "" {
		headers["Cookie"] = "PHPSESSID=" + a.session
	}
	hitsBefore := a.rateLimitHits
	status, body, err := a.get(ctx, pixivBase+"/ajax/illust/"+id, headers)
	if a.rateLimitHits > hitsBefore {
		// Slow down for the rest of the run after any 429.
		a.pThrottle.d = min(a.pThrottle.d*2, 10*time.Second)
		log.Printf("pixiv delay increased to %s", a.pThrottle.d)
	}
	if err != nil {
		return PixivEntry{}, err
	}
	e := PixivEntry{WithSession: a.session != "", CheckedAt: time.Now().UTC().Format(time.RFC3339)}

	var r struct {
		Error   bool            `json:"error"`
		Message string          `json:"message"`
		Body    json.RawMessage `json:"body"`
	}
	jsonErr := json.Unmarshal(body, &r)

	switch {
	case status == http.StatusNotFound:
		e.Error = "not found"
	case status != http.StatusOK:
		// 403 etc. usually means a blocked/expired session: do not cache, abort so the run can resume.
		return PixivEntry{}, fmt.Errorf("pixiv illust %s: HTTP %d (check PIXIV_PHPSESSID)", id, status)
	case jsonErr != nil:
		return PixivEntry{}, fmt.Errorf("pixiv illust %s: bad JSON: %w", id, jsonErr)
	case r.Error:
		e.Error = r.Message
		if e.Error == "" {
			e.Error = "pixiv API returned an error"
		}
	default:
		var b struct {
			UserID   string `json:"userId"`
			UserName string `json:"userName"`
		}
		if err := json.Unmarshal(r.Body, &b); err != nil || b.UserID == "" {
			e.Error = "response had no userId"
		} else {
			e.Available, e.UserID, e.UserName = true, b.UserID, b.UserName
		}
	}
	a.pixivCache[id] = e
	return e, nil
}

// ---------------------------------------------------------------------------
// Merge one card into the mapping
// ---------------------------------------------------------------------------

// fileResult is what authors.json knows about one author.
type fileResult struct {
	IDs          []string // pixiv account ids
	CreatorLinks []string // fanbox / patreon links
}

// fileAccounts looks an author name up in authors.json (empty result when there is no
// unambiguous entry). Entries that only list a pixiv artwork link cost one cached
// pixiv API call to learn the account.
func (a *App) fileAccounts(ctx context.Context, name string) (fileResult, error) {
	if a.fileIdx == nil {
		return fileResult{}, nil
	}
	key := normName(name)
	if key == "" {
		// names without letters or digits (".", ":)") all normalize to "": key them by the
		// exact name, or they would share one memo entry and inherit each other's accounts
		key = "\x00" + strings.TrimSpace(name)
	}
	if r, ok := a.fileMemo[key]; ok {
		return r, nil
	}
	var r fileResult
	entry, ambiguous := a.fileIdx.find(name)
	switch {
	case ambiguous:
		log.Printf("authors file: %q matches several entries, using pixiv API instead", name)
	case entry != nil:
		var artworks []string
		r.IDs, artworks = pixivRefs(entry.Links)
		for _, l := range entry.Links {
			if len(creatorHandles([]string{l})) > 0 {
				r.CreatorLinks = addUnique(r.CreatorLinks, l)
			}
		}
		for _, art := range artworks {
			pe, err := a.lookupPixiv(ctx, art)
			if err != nil {
				return fileResult{}, err
			}
			if pe.Available {
				r.IDs = addUnique(r.IDs, pe.UserID)
			}
		}
	}
	a.fileMemo[key] = r
	return r, nil
}

// linkPixiv records a pixiv account and ties it to an author tag (slug may be empty).
func (m *Mapping) linkPixiv(slug, userID, userName, source string) {
	pu := m.PixivUsers[userID]
	if pu == nil {
		pu = &PixivUser{ID: userID, URL: pixivBase + "/users/" + userID}
		m.PixivUsers[userID] = pu
	}
	if userName != "" {
		pu.Name = userName
	}
	pu.Sources = addUnique(pu.Sources, source)
	m.PixivUserURLToID[pu.URL] = pu.ID
	if au := m.Authors[slug]; slug != "" && au != nil {
		au.PixivUserIDs = addUnique(au.PixivUserIDs, pu.ID)
		au.PixivUserURLs = addUnique(au.PixivUserURLs, pu.URL)
		pu.AuthorSlugs = addUnique(pu.AuthorSlugs, slug)
		pu.KoikatsuAuthorURLs = addUnique(pu.KoikatsuAuthorURLs, au.KoikatsuURL)
	}
}

// refreshFromFile re-applies authors.json to every author already in the mapping, so
// authors from earlier runs (or newly added file entries) pick up accounts and links
// without re-crawling.
func (a *App) refreshFromFile(ctx context.Context) error {
	if a.fileIdx == nil {
		return nil
	}
	for slug, au := range a.mapping.Authors {
		r, err := a.fileAccounts(ctx, au.Name)
		if err != nil {
			return err
		}
		au.CreatorLinks = r.CreatorLinks
		for _, id := range r.IDs {
			a.mapping.linkPixiv(slug, id, "", "authors.json")
		}
	}
	return nil
}

func (a *App) process(ctx context.Context, c Card) error {
	m := a.mapping
	post := &Post{
		PixivID:     c.PixivID,
		PixivURL:    pixivBase + "/artworks/" + c.PixivID,
		KoikatsuURL: koikatsuBase + "/contents/pixiv-" + c.PixivID,
		Title:       c.Title,
		AuthorName:  c.Author,
	}

	var slug string
	if c.Author != "" {
		ae, err := a.lookupAuthor(ctx, c.Author)
		if err != nil {
			return err
		}
		if ae.Found {
			slug = ae.Slug
			post.AuthorSlug = slug
			au := m.Authors[slug]
			if au == nil {
				au = &Author{Name: ae.Name, Slug: slug, KoikatsuURL: authorURL(slug)}
				m.Authors[slug] = au
			}
			m.AuthorURLToSlug[au.KoikatsuURL] = slug
		}
	}

	// The work (series) shown above the title, e.g. "Blue Archive".
	post.WorkName = c.Work

	link := func(userID, userName, source string) { m.linkPixiv(slug, userID, userName, source) }
	finish := func() {
		m.Posts[c.PixivID] = post
		m.KoikatsuToPixiv[post.KoikatsuURL] = post.PixivURL
	}

	// 1. authors.json: no pixiv request needed for the post.
	if c.Author != "" {
		fr, err := a.fileAccounts(ctx, c.Author)
		if err != nil {
			return err
		}
		if au := m.Authors[slug]; slug != "" && au != nil {
			au.CreatorLinks = fr.CreatorLinks
		}
		if len(fr.IDs) > 0 {
			for _, id := range fr.IDs {
				link(id, "", "authors.json")
			}
			if pe, ok := a.pixivCache[c.PixivID]; ok && pe.Available {
				// Already looked up in an earlier run: free precision for this post.
				link(pe.UserID, pe.UserName, "pixiv_api")
				post.PixivUserID = pe.UserID
			} else if len(fr.IDs) == 1 {
				post.PixivUserID = fr.IDs[0]
			}
			finish()
			return nil
		}
	}

	// 2. Fallback: pixiv API.
	if a.oncePerAuthor && slug != "" {
		if au := m.Authors[slug]; len(au.PixivUserIDs) > 0 {
			if _, cached := a.pixivCache[c.PixivID]; !cached {
				if len(au.PixivUserIDs) == 1 {
					post.PixivUserID = au.PixivUserIDs[0]
				}
				finish()
				return nil
			}
		}
	}

	pe, err := a.lookupPixiv(ctx, c.PixivID)
	if err != nil {
		return err
	}
	if pe.Available {
		post.PixivUserID = pe.UserID
		link(pe.UserID, pe.UserName, "pixiv_api")
	} else {
		log.Printf("pixiv %s unavailable: %s", c.PixivID, pe.Error)
	}
	finish()
	return nil
}

// ---------------------------------------------------------------------------
// main
// ---------------------------------------------------------------------------

func run() (err error) {
	envFile := flag.String("env", ".env", "path to .env file")
	dataDir := flag.String("data", "data", "directory for mapping.json")
	cacheDir := flag.String("cache", "cache", "directory for cache files")
	startPage := flag.Int("start-page", 1, "first listing page")
	maxPages := flag.Int("max-pages", 0, "stop after this many pages (0 = no limit)")
	full := flag.Bool("full", false, "ignore stored newest post and crawl everything (caches still apply)")
	kDelay := flag.Duration("koikatsu-delay", 500*time.Millisecond, "delay between koikatsucards requests")
	once := flag.Bool("pixiv-once-per-author", false, "skip pixiv lookups for authors whose pixiv account is already known (far fewer requests)")
	pDelay := flag.Duration("pixiv-delay", 2*time.Second, "delay between pixiv requests")
	pawFile := flag.String("pawchive-file", "", "use this local creators.json instead of downloading")
	pawCache := flag.String("pawchive-cache", "", "pawchive creators cache path (default <cache>/pawchive_creators.json)")
	pawMaxAge := flag.Duration("pawchive-max-age", 24*time.Hour, "re-download the pawchive creators list when the cache is older than this")
	pixivLinks := flag.Bool("pixiv-links", true, "read every pixiv profile and keep its links; fanbox/patreon links then decide the pawchive match")
	pixivLinksMax := flag.Int("pixiv-links-max", 0, "read at most this many profiles per run (0 = no limit)")
	pixivLinksAge := flag.Duration("pixiv-links-max-age", 30*24*time.Hour, "read a profile again when it was read longer ago than this (0 = never)")
	noPawchive := flag.Bool("no-pawchive", false, "skip the pawchive matching step")
	bepisCards := flag.String("bepis-cards", "", "bepis cards cache, fetched from db.bepis.moe and read by kk-mod-scraper (default <cache>/bepis/cards.json)")
	bepisRetry := flag.Bool("bepis-retry-failed", false, "fetch again the bepis cards the site refused earlier")
	bepisRPS := flag.Float64("bepis-rps", 5, "most requests per second to db.bepis.moe (shared by all workers)")
	bepisWorkers := flag.Int("bepis-workers", 4, "parallel requests to db.bepis.moe")
	bepisSaveEvery := flag.Int("bepis-save-every", 500, "write the bepis cards cache to disk every N cards")
	bepisMaxCards := flag.Int("bepis-max-cards", 30000, "most bepis cards fetched per run (0 = no limit); the rest follows next run")
	lookupPath := flag.String("lookup", "", "write the lookup file for the browser extension to this path (not written when empty)")
	lookupOnly := flag.Bool("lookup-only", false, "only build the lookup file from the saved <data>/mapping.json (no network) and exit; needs -lookup")
	noAuthorsWrite := flag.Bool("no-authors-write", false, "do not update the authors file at the end of the run")
	namesSrc := flag.String("names-url", namesURL, "names.json (pepper-scene-hasher): archive names -> authors.json names")
	namesFile := flag.String("names-file", "", "use this local names.json instead of downloading")
	rentrySrc := flag.String("rentry-url", sceneArchiveURL, "Scene Archive rentry page")
	rentryFile := flag.String("rentry-file", "", "use this local saved rentry HTML instead of downloading")
	sourceMaxAge := flag.Duration("source-max-age", 24*time.Hour, "re-download names.json and the rentry page when older than this")
	pixivIcons := flag.Bool("pixiv-icons", true, "look up pixiv profile icons for authors without a pawchive account")
	tgTags := flag.String("telegram-tags", "", "text file with the channel's #hashtags (default <data>/telegram_tags.txt)")
	overridesPath := flag.String("overrides", "", `manual corrections by authors.json row name: {"name": {"telegram": ..., "pawchive": ...}} (default <data>/overrides.json)`)
	kknowURL := flag.String("kknow-search", kknowSearchURL, "kknow.cc search whose posts are read for pixiv user links (only its query and ordering are used)")
	kknowPages := flag.Int("kknow-max-pages", 30, "most result pages read per kknow.cc search")
	kknowDelay := flag.Duration("kknow-delay", 1500*time.Millisecond, "pause between requests to kknow.cc")
	noKknow := flag.Bool("no-kknow", false, "skip searching kknow.cc for pixiv accounts")
	noBepis := flag.Bool("no-bepis", false, "skip the bepis step (fetching the cards and matching authors)")
	authorsFile := flag.String("authors-file", "", "authors list: read at the start of the run and rewritten at the end (default <data>/authors.json)")
	tgChats := flag.String("telegram-chats", "", "JSON file with the Telegram chat links to export for pixiv links (default <data>/telegram_chats.json; the step is skipped when the file does not exist)")
	tgOut := flag.String("telegram-out", "", "where the pixiv users and posts found in those chats are saved (default <data>/telegram_pixiv.json)")
	tgSession := flag.String("telegram-session", "", "directory of the Telegram login session (default $TELEGRAM_SESSION_DIR or telegram_session; a tdl data dir such as ~/.tdl/data reuses a tdl login)")
	noTelegram := flag.Bool("no-telegram", false, "skip the Telegram chat export")
	tgOnly := flag.Bool("telegram-only", false, "only run the Telegram chat export, then exit")
	flag.Parse()
	if *lookupOnly {
		if *lookupPath == "" {
			return errors.New("-lookup-only needs -lookup <path>")
		}
		m := &Mapping{}
		if err := loadJSON(filepath.Join(*dataDir, "mapping.json"), m); err != nil {
			return fmt.Errorf("loading mapping: %w", err)
		}
		m.init()
		tgPath := *tgOut
		if tgPath == "" {
			tgPath = filepath.Join(*dataDir, "telegram_pixiv.json")
		}
		tg := &tgExport{}
		if err := loadJSON(tgPath, tg); err != nil {
			return fmt.Errorf("loading %s: %w", tgPath, err)
		}
		return writeLookup(m, tg, *lookupPath)
	}
	if *bepisCards == "" {
		*bepisCards = filepath.Join(*cacheDir, "bepis", "cards.json")
	}
	if *pawCache == "" {
		*pawCache = filepath.Join(*cacheDir, "pawchive_creators.json")
	}

	loadEnv(*envFile)

	if *tgOut == "" {
		*tgOut = filepath.Join(*dataDir, "telegram_pixiv.json")
	}
	if *tgOnly || !*noTelegram {
		if err := telegramStep(*dataDir, *tgChats, *tgOut, *tgSession, *tgOnly); err != nil {
			if *tgOnly || errors.Is(err, context.Canceled) {
				return err
			}
			log.Printf("telegram: %v (continuing without it)", err)
		}
	}
	if *tgOnly {
		return nil
	}

	a := &App{
		http:          &http.Client{Timeout: 30 * time.Second},
		session:       os.Getenv("PIXIV_PHPSESSID"),
		kThrottle:     throttle{d: *kDelay},
		pThrottle:     throttle{d: *pDelay},
		oncePerAuthor: *once,
		mapping:       &Mapping{},
		authorCache:   map[string]TagEntry{},
		pixivCache:    map[string]PixivEntry{},
		mappingPath:   filepath.Join(*dataDir, "mapping.json"),
		authorPath:    filepath.Join(*cacheDir, "authors.json"),
		pixivPath:     filepath.Join(*cacheDir, "pixiv", "posts.json"),
		profilePath:   filepath.Join(*cacheDir, "pixiv", "profiles.json"),
		kknowPath:     filepath.Join(*cacheDir, "pixiv", "kknow.json"),
		kknow:         &KknowCache{},
		tgPixiv:       &tgExport{},
	}
	if a.session == "" {
		log.Println("warning: PIXIV_PHPSESSID not set; restricted works will be unavailable")
	}
	if err := errors.Join(
		loadJSON(a.mappingPath, a.mapping),
		loadJSON(a.authorPath, &a.authorCache),
		loadJSON(a.pixivPath, &a.pixivCache),
		loadJSON(a.profilePath, &a.profileCache),
		loadJSON(a.kknowPath, a.kknow),
		loadJSON(*tgOut, a.tgPixiv),
	); err != nil {
		return fmt.Errorf("loading state: %w", err)
	}
	a.mapping.init()
	a.tgPixiv.init()
	a.fileMemo = map[string]fileResult{}
	if *authorsFile == "" {
		*authorsFile = filepath.Join(*dataDir, "authors.json")
	}
	if *tgTags == "" {
		*tgTags = filepath.Join(*dataDir, "telegram_tags.txt")
	}
	if *overridesPath == "" {
		*overridesPath = filepath.Join(*dataDir, "overrides.json")
	}
	ov, err := loadOverrides(*overridesPath)
	if err != nil {
		return fmt.Errorf("loading overrides: %w", err)
	}
	a.pawOverrides = ov.Pawchive
	if n := ov.count(); n > 0 {
		log.Printf("overrides: %d telegram, %d pawchive from %s", len(ov.Telegram), len(ov.Pawchive), *overridesPath)
	}
	if tt, err := loadTelegramTags(*tgTags, ov.Telegram); err != nil {
		return fmt.Errorf("loading telegram tags: %w", err)
	} else if len(tt.byNorm) > 0 || len(tt.overrides) > 0 {
		a.telegram = tt
		log.Printf("telegram: %d hashtags, %d overrides", len(tt.byNorm), len(tt.overrides))
	}
	if _, err := os.Stat(*authorsFile); err == nil {
		idx, err := loadAuthorsFile(*authorsFile)
		if err != nil {
			return fmt.Errorf("loading %s: %w", *authorsFile, err)
		}
		a.fileIdx = idx
		log.Printf("authors file: %d entries from %s", idx.count, *authorsFile)
	} else if os.IsNotExist(err) {
		log.Printf("no %s yet; all pixiv accounts will come from the pixiv API and the file is created at the end of the run", *authorsFile)
	} else {
		return err
	}
	if a.authorCache == nil {
		a.authorCache = map[string]TagEntry{}
	}
	if a.profileCache == nil {
		a.profileCache = map[string]ProfileEntry{}
	}
	if a.pixivCache == nil {
		a.pixivCache = map[string]PixivEntry{}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Always persist progress, even on error or Ctrl-C.
	defer func() {
		if serr := a.saveAll(); serr != nil {
			err = errors.Join(err, serr)
		}
	}()

	// Apply authors.json to authors from earlier runs too (no crawl needed).
	if err := a.refreshFromFile(ctx); err != nil {
		return err
	}

	prevNewest := a.mapping.NewestPost
	if *full {
		prevNewest = ""
	}
	newest := "" // newest post seen on page 1 of this run
	completed := false
	processed := 0

	page := *startPage
pages:
	for n := 0; *maxPages == 0 || n < *maxPages; n, page = n+1, page+1 {
		res, err := a.fetchPage(ctx, page)
		if err != nil {
			return err
		}
		if res.Links == 0 {
			log.Printf("page %d has no content links, stopping (end of results)", page)
			completed = true
			break
		}
		if len(res.Other) > 0 {
			log.Printf("page %d: skipping %d non-pixiv entries, e.g. %q", page, len(res.Other), res.Other[0])
		}
		if page == 1 && newest == "" && len(res.Cards) > 0 {
			newest = res.Cards[0].PixivID
		}
		for _, c := range res.Cards {
			if prevNewest != "" && c.PixivID == prevNewest {
				log.Printf("reached previous newest post %s, stopping", prevNewest)
				completed = true
				break pages
			}
			if err := a.process(ctx, c); err != nil {
				return err
			}
			processed++
		}
		log.Printf("page %d done: %d pixiv posts (%d new posts this run)", page, len(res.Cards), processed)
		if err := a.saveAll(); err != nil {
			return err
		}
		if res.NextKnown && !res.HasNext {
			log.Printf("page %d is the last page (Next is disabled), stopping", page)
			completed = true
			break
		}
	}

	// Only advance the bookmark after a clean, complete pass so an interrupted
	// run is resumed from the top next time (caches make that cheap).
	if completed && newest != "" {
		a.mapping.NewestPost = newest
	}
	// Pixiv accounts mentioned on kknow.cc get their profile read too (they are found before
	// the profiles are read). A failure only warns, and the accounts found so far are kept.
	if !*noKknow && ctx.Err() == nil {
		if err := a.runKknow(ctx, kknowOptions{SearchURL: *kknowURL, MaxPages: max(1, *kknowPages), Delay: *kknowDelay, Full: *full}); err != nil {
			log.Printf("warning: kknow step failed: %v", err)
		}
	}
	if *pixivLinks && ctx.Err() == nil {
		a.runPixivLinks(ctx, *pixivLinksAge, *pixivLinksMax)
	}
	// The accounts found on kknow.cc and in Telegram chats, whose profile is known now, become
	// authors.json entries before the pawchive and bepis matching, which use that list.
	if !*noKknow || len(a.tgPixiv.PixivUsers) > 0 {
		if n := a.addKknowAuthors(); n > 0 {
			log.Printf("kknow/telegram: %d pixiv accounts added to the authors list", n)
		}
	}
	// Pawchive matching is recomputed over the whole mapping every run
	// (favorited counts change), so it also covers posts from earlier runs.
	if !*noPawchive && ctx.Err() == nil {
		if err := a.runPawchive(ctx, *pawFile, *pawCache, *pawMaxAge); err != nil {
			return err
		}
	}
	// Bepis: first bring cache/bepis/cards.json up to date (kk-mod-scraper downloads that file),
	// then recompute the matching over the whole mapping, after the pixiv accounts are final.
	// A failure only warns: the rest is already done.
	if !*noBepis && ctx.Err() == nil {
		if err := a.runBepis(ctx, bepisOptions{
			CardsPath:   *bepisCards,
			RetryFailed: *bepisRetry,
			MaxCards:    *bepisMaxCards,
			Workers:     *bepisWorkers,
			SaveEvery:   *bepisSaveEvery,
			RPS:         *bepisRPS,
		}); err != nil {
			log.Printf("warning: bepis step failed: %v", err)
		}
	}
	if *lookupPath != "" && ctx.Err() == nil {
		if err := writeLookup(a.mapping, a.tgPixiv, *lookupPath); err != nil {
			return err
		}
	}
	if !*noAuthorsWrite && ctx.Err() == nil {
		src := a.loadAuthorSources(ctx, sourceOpts{
			NamesURL: *namesSrc, NamesFile: *namesFile,
			RentryURL: *rentrySrc, RentryFile: *rentryFile,
			CacheDir: *cacheDir, MaxAge: *sourceMaxAge,
		})
		rows := a.buildAuthorRows(ctx, src, *pixivIcons)
		if err := saveJSONIndent(*authorsFile, rows); err != nil {
			return err
		}
		dl, rec, ms := 0, 0, 0
		for _, r := range rows {
			if r.Links.Download != (DownloadLinks{}) {
				dl++
			}
			if r.IsRecommended {
				rec++
			}
			if r.MakesScenes {
				ms++
			}
		}
		log.Printf("authors file: %d authors (%d with download, %d recommended, %d make scenes) -> %s", len(rows), dl, rec, ms, *authorsFile)
	}
	log.Printf("finished: %d posts processed, newest=%s", processed, a.mapping.NewestPost)
	return nil
}

func main() {
	if err := run(); err != nil {
		if errors.Is(err, context.Canceled) {
			log.Println("interrupted; progress saved")
			return
		}
		log.Fatal(err)
	}
}

// saveKknow writes cache/pixiv/kknow.json (nothing when the step never ran).
func (a *App) saveKknow() error {
	if a.kknow == nil || a.kknowPath == "" {
		return nil
	}
	if a.kknow.Users == nil {
		a.kknow.Users = map[string]kknowUser{}
	}
	return saveJSON(a.kknowPath, a.kknow)
}
