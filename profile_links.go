package main

import (
	"context"
	"encoding/json"
	"html"
	"log"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

// ProfileLink is one link found on a pixiv profile.
type ProfileLink struct {
	URL    string `json:"url"`
	Source string `json:"source"` // "comment" (the description), "webpage" or "social:<network>"
}

// linkChars matches the characters of a URL inside free text (a description often puts
// full-width brackets or Japanese punctuation right after a link).
const linkChars = `[^\s<>"'「」『』（）()【】\[\]、。，]`

var (
	schemeURLRe = regexp.MustCompile(`(?i)https?://` + linkChars + `+`)
	// fanbox / patreon written without a scheme ("fanbox.cc/@name", "name.fanbox.cc")
	bareCreatorRe = regexp.MustCompile(`(?i)(?:^|[^a-z0-9/.@-])((?:[a-z0-9-]+\.)*(?:fanbox\.cc|patreon\.com)(?:/` + linkChars + `*)?)`)
	hrefRe        = regexp.MustCompile(`(?i)href\s*=\s*"([^"]*)"`)
)

// hrefTarget turns a link of the description's HTML into the real address: pixiv wraps
// external links as /jump.php?<escaped url> and uses relative links for its own pages.
func hrefTarget(h string) string {
	h = strings.TrimSpace(html.UnescapeString(h))
	switch {
	case h == "" || strings.HasPrefix(h, "#") || strings.HasPrefix(strings.ToLower(h), "javascript:"):
		return ""
	case strings.HasPrefix(h, "/"):
		h = pixivBase + h
	}
	u, err := url.Parse(h)
	if err != nil || u.Host == "" {
		return ""
	}
	if strings.HasSuffix(u.Path, "/jump.php") {
		q := strings.TrimPrefix(u.RawQuery, "url=")
		if t, err := url.QueryUnescape(q); err == nil && t != "" {
			return t
		}
		return ""
	}
	return h
}

// extractProfileLinks collects every link of a pixiv profile: the links of the description
// (from its HTML, then from its plain text, including fanbox / patreon addresses written
// without "https://"), the homepage and the social links. Each link appears once.
func extractProfileLinks(comment, commentHTML, webpage string, social map[string]string) []ProfileLink {
	var out []ProfileLink
	seen := map[string]bool{}
	add := func(raw, source string) {
		raw = strings.TrimRight(strings.TrimSpace(raw), ".,;:!?…")
		if raw == "" {
			return
		}
		if !strings.Contains(raw, "://") {
			raw = "https://" + raw
		}
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" || !strings.Contains(u.Hostname(), ".") {
			return
		}
		key := strings.ToLower(strings.TrimPrefix(strings.ToLower(u.Host), "www.") + u.RequestURI())
		key = strings.TrimSuffix(key, "/")
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, ProfileLink{URL: raw, Source: source})
	}
	for _, m := range hrefRe.FindAllStringSubmatch(commentHTML, -1) {
		add(hrefTarget(m[1]), "comment")
	}
	for _, l := range schemeURLRe.FindAllString(comment, -1) {
		add(l, "comment")
	}
	for _, m := range bareCreatorRe.FindAllStringSubmatch(comment, -1) {
		add(m[1], "comment")
	}
	add(webpage, "webpage")
	names := make([]string, 0, len(social))
	for n := range social {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		add(social[n], "social:"+n)
	}
	return out
}

// profileFromBody fills e from the body of pixiv's /ajax/user/<id>?full=1 response.
func profileFromBody(body json.RawMessage, e ProfileEntry) ProfileEntry {
	var b struct {
		Name        string          `json:"name"`
		ImageBig    string          `json:"imageBig"`
		ImageSmall  string          `json:"image"`
		Comment     string          `json:"comment"`
		CommentHTML string          `json:"commentHtml"`
		Webpage     string          `json:"webpage"`
		Social      json.RawMessage `json:"social"`
	}
	if err := json.Unmarshal(body, &b); err != nil {
		e.Error = "unreadable profile"
		return e
	}
	social := map[string]string{}
	var raw map[string]struct {
		URL string `json:"url"`
	}
	if json.Unmarshal(b.Social, &raw) == nil { // an empty "social" is [] and is skipped here
		for n, v := range raw {
			social[n] = v.URL
		}
	}
	e.Profile = true
	e.Comment, e.Webpage = b.Comment, b.Webpage
	e.Links = extractProfileLinks(b.Comment, b.CommentHTML, b.Webpage, social)
	img := b.ImageBig
	if img == "" {
		img = b.ImageSmall
	}
	if img == "" {
		e.Error = "no profile image"
	} else {
		e.Available, e.Name, e.Image = true, b.Name, img
	}
	return e
}

// profileLinkURLs returns the links found on the profiles of the given pixiv accounts.
func (m *Mapping) profileLinkURLs(ids []string) []string {
	var out []string
	for _, id := range ids {
		if pp := m.PixivProfiles[id]; pp != nil {
			for _, l := range pp.Links {
				out = append(out, l.URL)
			}
		}
	}
	return out
}

// corePixivAccounts lists the pixiv accounts of the mapping and of the authors file, with or
// without a pawchive match.
func (a *App) corePixivAccounts() map[string]bool {
	set := map[string]bool{}
	for id := range a.mapping.PixivUsers {
		set[id] = true
	}
	if a.fileIdx != nil {
		for _, e := range a.fileIdx.entries {
			users, _ := pixivRefs(e.Links)
			for _, id := range users {
				set[id] = true
			}
		}
	}
	return set
}

// profileAccounts lists every pixiv account to read: the core accounts plus those found on
// kknow.cc or in Telegram chats (their profile goes to the profile cache, which kk-mod-scraper
// reads; the mapping only gets it once the account has an entry in the authors list, see
// addKknowAuthors).
func (a *App) profileAccounts() []string {
	set := a.corePixivAccounts()
	for _, id := range a.extraAccounts() {
		set[id] = true
	}
	ids := make([]string, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// runPixivLinks reads the profile of every pixiv account (profiles already read within
// maxAge come from the cache) and stores its links in the mapping. limit > 0 caps the
// number of profile requests of this run. The links feed the pawchive matching: a
// fanbox / patreon link on a profile is a much better sign than a matching name.
func (a *App) runPixivLinks(ctx context.Context, maxAge time.Duration, limit int) {
	if a.profileCache == nil {
		a.profileCache = map[string]ProfileEntry{}
	}
	if a.mapping.PixivProfiles == nil {
		a.mapping.PixivProfiles = map[string]*PixivProfile{}
	}
	ids := a.profileAccounts()
	core := a.corePixivAccounts()
	fetched, withLinks, withCreator := 0, 0, 0
	for _, id := range ids {
		if ctx.Err() != nil {
			break
		}
		e, ok := a.profileCache[id]
		fresh := ok && e.Profile && (e.Available || e.WithSession || a.session == "")
		if fresh && maxAge > 0 {
			if t, err := time.Parse(time.RFC3339, e.CheckedAt); err != nil || time.Since(t) > maxAge {
				fresh = false
			}
		}
		if !fresh && (limit <= 0 || fetched < limit) {
			ne, err := a.fetchPixivProfile(ctx, id)
			if err != nil {
				log.Printf("warning: pixiv profile links stopped at %s: %v", id, err)
				break
			}
			e = ne
			fetched++
			if fetched%25 == 0 {
				if err := a.saveAll(); err != nil {
					log.Printf("warning: saving progress: %v", err)
				}
				log.Printf("pixiv profiles: %d read so far", fetched)
			}
		}
		if !e.Profile {
			continue
		}
		pp := &PixivProfile{Links: e.Links, CheckedAt: e.CheckedAt}
		if pp.Links == nil {
			pp.Links = []ProfileLink{}
		}
		if !e.Available && e.Error != "no profile image" {
			pp.Error = e.Error
		}
		if core[id] {
			a.mapping.PixivProfiles[id] = pp
		}
		if len(e.Links) > 0 {
			withLinks++
		}
		urls := make([]string, 0, len(e.Links))
		for _, l := range e.Links {
			urls = append(urls, l.URL)
		}
		if len(creatorHandles(urls)) > 0 {
			withCreator++
		}
	}
	log.Printf("pixiv profiles: %d read this run; %d/%d accounts have links, %d with a fanbox/patreon link",
		fetched, withLinks, len(ids), withCreator)
}

// profileFileLinks returns the first mega.nz link and the first google drive link found on
// the profiles of the given pixiv accounts ("" when there is none).
func (m *Mapping) profileFileLinks(ids []string) (mega, drive string) {
	for _, raw := range m.profileLinkURLs(ids) {
		u, err := url.Parse(raw)
		if err != nil {
			continue
		}
		h := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
		switch {
		case mega == "" && (h == "mega.nz" || h == "mega.co.nz"):
			mega = raw
		case drive == "" && (h == "drive.google.com" || h == "drive.usercontent.google.com"):
			drive = raw
		}
	}
	return mega, drive
}
