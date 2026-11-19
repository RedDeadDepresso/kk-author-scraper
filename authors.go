package main

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	sceneArchiveURL = "https://rentry.co/SceneArchive"
	namesURL        = "https://raw.githubusercontent.com/RedDeadDepresso/pepper-scene-hasher/refs/heads/main/config/names.json"
)

// ---------------------------------------------------------------------------
// Output (data/authors.json, which is also the scraper's author list)
// ---------------------------------------------------------------------------

type AuthorLinks struct {
	Pixiv         string `json:"pixiv"`
	Download      string `json:"download"`
	Pawchive      string `json:"pawchive"`
	Bepis         string `json:"bepis"`
	Koikatsucards string `json:"koikatsucards"`
}

// count returns how many of the links are not empty.
func (l AuthorLinks) count() int {
	n := 0
	for _, v := range []string{l.Pixiv, l.Download, l.Pawchive, l.Bepis, l.Koikatsucards} {
		if v != "" {
			n++
		}
	}
	return n
}

// AuthorRow is one entry of authors.json. Missing values are "" / false / [].
type AuthorRow struct {
	Name          string      `json:"name"`
	Alias         []string    `json:"alias"`
	Series        []string    `json:"series"`
	IsRecommended bool        `json:"isRecommended"`
	MakeScenes    bool        `json:"makeScenes"`
	Favorited     int         `json:"favorited"`
	Icon          string      `json:"icon"`
	Banner        string      `json:"banner"`
	Links         AuthorLinks `json:"links"`
}

// ---------------------------------------------------------------------------
// Cached downloads
// ---------------------------------------------------------------------------

// fetchCached returns the body of rawURL, cached on disk. A fresh cache means no
// request; a failed request falls back to a stale cache when there is one.
func (a *App) fetchCached(ctx context.Context, label, rawURL, cachePath string, maxAge time.Duration, validate func([]byte) error) ([]byte, error) {
	st, statErr := os.Stat(cachePath)
	if statErr == nil && time.Since(st.ModTime()) < maxAge {
		if b, err := os.ReadFile(cachePath); err == nil && validate(b) == nil {
			return b, nil
		}
	}
	status, body, err := a.get(ctx, rawURL, nil)
	if err == nil && status != http.StatusOK {
		err = fmt.Errorf("%s: HTTP %d", label, status)
	}
	if err == nil {
		err = validate(body) // reject challenge/error pages before caching them
	}
	if err == nil {
		if os.MkdirAll(filepath.Dir(cachePath), 0o755) == nil {
			tmp := cachePath + ".tmp"
			if os.WriteFile(tmp, body, 0o644) == nil {
				os.Rename(tmp, cachePath)
			}
		}
		return body, nil
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if statErr == nil {
		if b, rerr := os.ReadFile(cachePath); rerr == nil && validate(b) == nil {
			log.Printf("warning: %v; using stale %s cache", err, label)
			return b, nil
		}
	}
	return nil, err
}

// ---------------------------------------------------------------------------
// Scene Archive (rentry): download links and the crown (recommended) marker
// ---------------------------------------------------------------------------

// DownloadLink is one link of the Scene Archive page.
type DownloadLink struct {
	Text  string
	URL   string
	Crown bool // preceded by a crown emoji = recommended
}

var (
	anchorRe = regexp.MustCompile(`(?s)<a\s[^>]*?href="([^"]+)"[^>]*>(.*?)</a>`)
	tagRe    = regexp.MustCompile(`<[^>]+>`)
	// the crown sits right before the link's wrapping <span>
	crownRe = regexp.MustCompile("\U0001F451\\s*(?:<span[^>]*>)?\\s*$")
)

func isDownloadHost(h string) bool {
	h = strings.ToLower(h)
	return strings.Contains(h, "buzzheavier") || strings.HasPrefix(h, "fileditch")
}

// parseSceneArchive extracts file-host links from the rendered rentry page.
// Struck-through (<del>) entries are removed archives and are skipped.
func parseSceneArchive(page string) []DownloadLink {
	var out []DownloadLink
	for _, loc := range anchorRe.FindAllStringSubmatchIndex(page, -1) {
		href := html.UnescapeString(page[loc[2]:loc[3]])
		u, err := url.Parse(href)
		if err != nil || !isDownloadHost(u.Hostname()) {
			continue
		}
		inner := page[loc[4]:loc[5]]
		if strings.Contains(inner, "<del") {
			continue
		}
		text := strings.TrimSpace(html.UnescapeString(tagRe.ReplaceAllString(inner, "")))
		if text == "" {
			continue
		}
		from := loc[0] - 300
		if from < 0 {
			from = 0
		}
		out = append(out, DownloadLink{Text: text, URL: href, Crown: crownRe.MatchString(page[from:loc[0]])})
	}
	return out
}

// ---------------------------------------------------------------------------
// names.json (pepper-scene-hasher): which authors make scenes + rentry name -> author name
// ---------------------------------------------------------------------------

var nameSuffixRe = regexp.MustCompile(`_\d{4}(?:-\d{2}-\d{2})?$`)

// parseNames reads names.json. Keys are archive folder names ("AnnaAnon_2026") and
// values are the authors.json name (or null). It returns the set of normalized
// values (authors that make scenes) and normalized key-without-year -> value.
func parseNames(b []byte) (makeScenes map[string]bool, keyToAuthor map[string]string, err error) {
	var raw map[string]*string
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, nil, err
	}
	makeScenes = map[string]bool{}
	keyToAuthor = map[string]string{}
	for k, v := range raw {
		if v == nil || strings.TrimSpace(*v) == "" {
			continue
		}
		makeScenes[normName(*v)] = true
		keyToAuthor[normName(nameSuffixRe.ReplaceAllString(k, ""))] = *v
	}
	return makeScenes, keyToAuthor, nil
}

// authorSources holds what the rentry page and names.json tell us.
type authorSources struct {
	makeScenes map[string]bool         // normalized authors.json names that make scenes
	byAuthor   map[string]DownloadLink // normalized authors.json name -> link (via names.json)
	byText     map[string]DownloadLink // normalized rentry link text -> link
}

func newSources() *authorSources {
	return &authorSources{
		makeScenes: map[string]bool{},
		byAuthor:   map[string]DownloadLink{},
		byText:     map[string]DownloadLink{},
	}
}

func buildSources(links []DownloadLink, makeScenes map[string]bool, keyToAuthor map[string]string) *authorSources {
	s := newSources()
	for k := range makeScenes {
		s.makeScenes[k] = true
	}
	for _, l := range links {
		k := normName(l.Text)
		if k == "" {
			continue
		}
		if _, dup := s.byText[k]; !dup {
			s.byText[k] = l
		}
		if author, ok := keyToAuthor[k]; ok {
			ak := normName(author)
			if _, dup := s.byAuthor[ak]; !dup {
				s.byAuthor[ak] = l
			}
		}
	}
	return s
}

// find looks up the candidate names (most reliable first). The authors.json name
// route through names.json wins over a plain text match.
func (s *authorSources) find(names ...string) (makeScenes bool, dl DownloadLink, ok bool) {
	for _, n := range names {
		k := normName(n)
		if k == "" {
			continue
		}
		if s.makeScenes[k] {
			makeScenes = true
		}
		if d, f := s.byAuthor[k]; f && !ok {
			dl, ok = d, true
		}
	}
	if !ok {
		for _, n := range names {
			if k := normName(n); k != "" {
				if d, f := s.byText[k]; f {
					return makeScenes, d, true
				}
			}
		}
	}
	return makeScenes, dl, ok
}

type sourceOpts struct {
	NamesURL, NamesFile   string
	RentryURL, RentryFile string
	CacheDir              string
	MaxAge                time.Duration
}

// loadAuthorSources never fails the run: missing sources just leave
// download / isRecommended / makeScenes empty, with a warning.
func (a *App) loadAuthorSources(ctx context.Context, o sourceOpts) *authorSources {
	var links []DownloadLink
	var makeScenes map[string]bool
	var keyToAuthor map[string]string

	validateNames := func(b []byte) error { _, _, err := parseNames(b); return err }
	validateRentry := func(b []byte) error {
		if !strings.Contains(string(b), "buzzheavier") {
			return fmt.Errorf("page has no download links")
		}
		return nil
	}

	var nb []byte
	var err error
	if o.NamesFile != "" {
		nb, err = os.ReadFile(o.NamesFile)
	} else {
		nb, err = a.fetchCached(ctx, "names.json", o.NamesURL, filepath.Join(o.CacheDir, "names.json"), o.MaxAge, validateNames)
	}
	if err == nil {
		makeScenes, keyToAuthor, err = parseNames(nb)
	}
	if err != nil {
		log.Printf("warning: names.json unavailable (%v): makeScenes will be false and downloads unmatched", err)
	}

	var rb []byte
	if o.RentryFile != "" {
		rb, err = os.ReadFile(o.RentryFile)
	} else {
		rb, err = a.fetchCached(ctx, "Scene Archive page", o.RentryURL, filepath.Join(o.CacheDir, "scene_archive.html"), o.MaxAge, validateRentry)
	}
	if err != nil {
		log.Printf("warning: Scene Archive page unavailable (%v): download and isRecommended will be empty", err)
	} else {
		links = parseSceneArchive(string(rb))
	}

	src := buildSources(links, makeScenes, keyToAuthor)
	log.Printf("author sources: %d archive links, %d linked via names.json, %d makeScenes names", len(links), len(src.byAuthor), len(src.makeScenes))
	return src
}

// ---------------------------------------------------------------------------
// Pixiv profile icons (only for authors without a pawchive account)
// ---------------------------------------------------------------------------

// ProfileEntry caches a pixiv /ajax/user lookup.
type ProfileEntry struct {
	Available   bool   `json:"available"`
	Name        string `json:"name,omitempty"`
	Image       string `json:"image,omitempty"`
	Error       string `json:"error,omitempty"`
	WithSession bool   `json:"with_session"`
	CheckedAt   string `json:"checked_at"`
}

func (a *App) pixivProfile(ctx context.Context, id string) (ProfileEntry, error) {
	if a.profileCache == nil {
		a.profileCache = map[string]ProfileEntry{}
	}
	if e, ok := a.profileCache[id]; ok && (e.Available || e.WithSession || a.session == "") {
		return e, nil
	}
	if err := a.pThrottle.wait(ctx); err != nil {
		return ProfileEntry{}, err
	}
	headers := map[string]string{"Referer": pixivBase + "/", "Accept": "application/json"}
	if a.session != "" {
		headers["Cookie"] = "PHPSESSID=" + a.session
	}
	hitsBefore := a.rateLimitHits
	status, body, err := a.get(ctx, pixivBase+"/ajax/user/"+id+"?full=1", headers)
	if a.rateLimitHits > hitsBefore {
		a.pThrottle.d = min(a.pThrottle.d*2, 10*time.Second)
		log.Printf("pixiv delay increased to %s", a.pThrottle.d)
	}
	if err != nil {
		return ProfileEntry{}, err
	}
	e := ProfileEntry{WithSession: a.session != "", CheckedAt: time.Now().UTC().Format(time.RFC3339)}
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
		return ProfileEntry{}, fmt.Errorf("pixiv user %s: HTTP %d (check PIXIV_PHPSESSID)", id, status)
	case jsonErr != nil:
		return ProfileEntry{}, fmt.Errorf("pixiv user %s: bad JSON: %w", id, jsonErr)
	case r.Error:
		e.Error = r.Message
		if e.Error == "" {
			e.Error = "pixiv API returned an error"
		}
	default:
		var b struct {
			Name       string `json:"name"`
			ImageBig   string `json:"imageBig"`
			ImageSmall string `json:"image"`
		}
		img := ""
		if err := json.Unmarshal(r.Body, &b); err == nil {
			img = b.ImageBig
			if img == "" {
				img = b.ImageSmall
			}
		}
		if img == "" {
			e.Error = "no profile image"
		} else {
			e.Available, e.Name, e.Image = true, b.Name, img
		}
	}
	a.profileCache[id] = e
	return e, nil
}

// ---------------------------------------------------------------------------
// isSeries reports whether a post's work name is a real series; placeholders such as
// "Original" or "Unknown" are not listed in the series field.
func isSeries(work string) bool {
	switch strings.ToLower(strings.TrimSpace(work)) {
	case "", "original", "unknown", "unknown work":
		return false
	}
	return true
}

// Building the authors list
// ---------------------------------------------------------------------------

func pawchiveIcon(service, id string) string { return pawchiveBase + "/icons/" + service + "/" + id }
func pawchiveBanner(service, id string) string {
	return pawchiveBase + "/banners/" + service + "/" + id
}

// aliasSet collects display aliases, skipping the name itself and case-insensitive duplicates.
type aliasSet struct {
	seen map[string]bool
	list []string
}

func newAliasSet(name string) *aliasSet {
	s := &aliasSet{seen: map[string]bool{}, list: []string{}}
	s.seen[strings.ToLower(strings.TrimSpace(name))] = true
	return s
}

func (s *aliasSet) add(v string) {
	v = strings.TrimSpace(v)
	// only case-insensitive exact duplicates are dropped: "Anna Anon" and "anna-anon" are
	// useful aliases of "AnnaAnon" for searching
	k := strings.ToLower(v)
	if v == "" || s.seen[k] {
		return
	}
	s.seen[k] = true
	s.list = append(s.list, v)
}

// primaryPixiv picks the account to show for an author: the one matching its
// pawchive fanbox id, else the one with the most koikatsucards posts.
func (m *Mapping) primaryPixiv(au *Author, postCounts map[string]int) string {
	ids := append([]string(nil), au.PixivUserIDs...)
	if len(ids) == 0 {
		return ""
	}
	pawID := ""
	if au.Pawchive != nil && au.Pawchive.Service == "fanbox" {
		pawID = au.Pawchive.ID
	}
	sort.Slice(ids, func(i, j int) bool {
		if (ids[i] == pawID) != (ids[j] == pawID) {
			return ids[i] == pawID
		}
		if postCounts[ids[i]] != postCounts[ids[j]] {
			return postCounts[ids[i]] > postCounts[ids[j]]
		}
		if len(ids[i]) != len(ids[j]) {
			return len(ids[i]) < len(ids[j])
		}
		return ids[i] < ids[j]
	})
	if pu := m.PixivUsers[ids[0]]; pu != nil {
		return pu.URL
	}
	return pixivBase + "/users/" + ids[0]
}

// bepisByCards orders bepis names by card count (desc), then name.
func (m *Mapping) bepisByCards(names []string) []string {
	out := append([]string(nil), names...)
	count := func(n string) int {
		if ba := m.BepisAuthors[n]; ba != nil {
			return ba.CardCount
		}
		return 0
	}
	sort.Slice(out, func(i, j int) bool {
		if count(out[i]) != count(out[j]) {
			return count(out[i]) > count(out[j])
		}
		return out[i] < out[j]
	})
	return out
}

func (m *Mapping) bepisURL(name string) string {
	if ba := m.BepisAuthors[name]; ba != nil && ba.URL != "" {
		return ba.URL
	}
	return bepisAuthorURL(name)
}

// buildAuthorRows creates one row per koikatsucards author, one per bepis author that
// is not linked to a koikatsucards author (case variants are merged), and one per
// authors.json entry that matched neither.
func (a *App) buildAuthorRows(ctx context.Context, src *authorSources, lookupIcons bool) []AuthorRow {
	m := a.mapping
	if src == nil {
		src = newSources()
	}

	// series per author (work names of their posts, most frequent first) and posts per pixiv account
	seriesCount := map[string]map[string]int{}
	postCounts := map[string]int{}
	for _, p := range m.Posts {
		if p.PixivUserID != "" {
			postCounts[p.PixivUserID]++
		}
		w := strings.TrimSpace(p.WorkName)
		if p.AuthorSlug == "" || !isSeries(w) {
			continue
		}
		if seriesCount[p.AuthorSlug] == nil {
			seriesCount[p.AuthorSlug] = map[string]int{}
		}
		seriesCount[p.AuthorSlug][w]++
	}

	iconsDisabled := false
	icon := func(userID string) string {
		if e, ok := a.profileCache[userID]; ok && e.Available {
			return e.Image
		}
		if !lookupIcons || iconsDisabled {
			return ""
		}
		e, err := a.pixivProfile(ctx, userID)
		if err != nil {
			log.Printf("warning: pixiv icon lookup failed (%v); skipping further icon lookups", err)
			iconsDisabled = true
			return ""
		}
		if e.Available {
			return e.Image
		}
		return ""
	}

	var rows []AuthorRow

	// authors.json entries that already have a row (matched by name/alias or by pixiv account)
	usedEntries := map[*fileAuthor]bool{}
	knownPixiv := map[string]bool{}
	markUsed := func(name string) {
		if a.fileIdx != nil {
			for _, e := range a.fileIdx.findAll(name) {
				usedEntries[e] = true
			}
		}
	}

	slugs := make([]string, 0, len(m.Authors))
	for slug := range m.Authors {
		slugs = append(slugs, slug)
	}
	sort.Strings(slugs)

	for _, slug := range slugs {
		au := m.Authors[slug]
		markUsed(au.Name)
		for _, id := range au.PixivUserIDs {
			knownPixiv[id] = true
		}
		var entry *fileAuthor
		if a.fileIdx != nil {
			entry, _ = a.fileIdx.find(au.Name)
		}
		name := au.Name
		lookupNames := []string{au.Name}
		if entry != nil && entry.Name != "" {
			name = entry.Name
			lookupNames = []string{entry.Name, au.Name}
		}

		aliases := newAliasSet(name)
		if entry != nil {
			for _, al := range entry.Alias {
				aliases.add(al)
			}
		}
		aliases.add(au.Name)
		bepisNames := make([]string, 0, len(au.Bepis))
		for _, b := range au.Bepis {
			bepisNames = append(bepisNames, b.Name)
			aliases.add(b.Name)
		}

		row := AuthorRow{
			Name:   name,
			Alias:  aliases.list,
			Series: []string{},
			Links:  AuthorLinks{Koikatsucards: authorURL(slug)},
		}
		if names := m.bepisByCards(bepisNames); len(names) > 0 {
			row.Links.Bepis = m.bepisURL(names[0])
		}
		row.Links.Pixiv = m.primaryPixiv(au, postCounts)

		makeScenes, dl, ok := src.find(lookupNames...)
		row.MakeScenes = makeScenes
		if ok {
			row.Links.Download, row.IsRecommended = dl.URL, dl.Crown
		}

		if au.Pawchive != nil {
			row.Links.Pawchive = au.Pawchive.URL
			row.Favorited = au.Pawchive.Favorited
			row.Icon = pawchiveIcon(au.Pawchive.Service, au.Pawchive.ID)
			row.Banner = pawchiveBanner(au.Pawchive.Service, au.Pawchive.ID)
		} else if row.Links.Pixiv != "" {
			row.Icon = icon(strings.TrimPrefix(row.Links.Pixiv, pixivBase+"/users/"))
		}

		counts := seriesCount[slug]
		for w := range counts {
			row.Series = append(row.Series, w)
		}
		sort.Slice(row.Series, func(i, j int) bool {
			if counts[row.Series[i]] != counts[row.Series[j]] {
				return counts[row.Series[i]] > counts[row.Series[j]]
			}
			return row.Series[i] < row.Series[j]
		})
		rows = append(rows, row)
	}

	// bepis authors with no koikatsucards author; names differing only by case/punctuation are merged
	kcRows := map[string]int{} // exact name -> index of its koikatsucards row
	for i := range rows {
		if rows[i].Links.Koikatsucards != "" {
			kcRows[rows[i].Name] = i
		}
	}
	groups := map[string][]string{}
	for name, ba := range m.BepisAuthors {
		if len(ba.KoikatsuAuthorURLs) > 0 {
			continue
		}
		k := normName(name)
		if k == "" {
			// names without letters or digits ("." or ":)") cannot be matched by
			// normalized name; an identical name on a koikatsucards row is the same author
			if i, ok := kcRows[name]; ok {
				if rows[i].Links.Bepis == "" {
					rows[i].Links.Bepis = m.bepisURL(name)
				}
				markUsed(name)
				continue
			}
			k = name
		}
		groups[k] = append(groups[k], name)
	}
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		names := m.bepisByCards(groups[k])
		for _, n := range names {
			markUsed(n)
		}
		row := AuthorRow{
			Name:   names[0],
			Alias:  []string{},
			Series: []string{},
			Links:  AuthorLinks{Bepis: m.bepisURL(names[0])},
		}
		aliases := newAliasSet(names[0])
		for _, n := range names[1:] {
			aliases.add(n)
		}
		if a.fileIdx != nil { // keep what the file already has for this author: names, aliases, pixiv account
			var ids []string
			for _, n := range names {
				for _, e := range a.fileIdx.findAll(n) {
					aliases.add(e.Name)
					for _, al := range e.Alias {
						aliases.add(al)
					}
					u, _ := pixivRefs(e.Links)
					ids = append(ids, u...)
				}
			}
			if len(ids) > 0 {
				row.Links.Pixiv = pixivBase + "/users/" + ids[0]
				row.Icon = icon(ids[0])
			}
		}
		row.Alias = aliases.list
		makeScenes, dl, ok := src.find(names...)
		row.MakeScenes = makeScenes
		if ok {
			row.Links.Download, row.IsRecommended = dl.URL, dl.Crown
		}
		rows = append(rows, row)
	}

	// authors.json entries matched by neither koikatsucards nor bepis
	if a.fileIdx != nil {
		for _, e := range a.fileIdx.entries {
			if usedEntries[e] || strings.TrimSpace(e.Name) == "" {
				continue
			}
			ids, _ := pixivRefs(e.Links) // artwork links would need a pixiv API call per entry: skipped
			shared := false
			for _, id := range ids {
				if knownPixiv[id] {
					shared = true
				}
			}
			if shared {
				continue
			}
			row := AuthorRow{Name: e.Name, Alias: []string{}, Series: []string{}}
			aliases := newAliasSet(e.Name)
			for _, al := range e.Alias {
				aliases.add(al)
			}
			row.Alias = aliases.list

			var paw *PawchiveMatch
			if a.pawIdx != nil {
				paw = a.pawIdx.resolve(ids, []string{e.Name}, creatorHandles(e.Links))
			}
			pawID := ""
			if paw != nil && paw.Service == "fanbox" {
				pawID = paw.ID
			}
			primary := ""
			for _, id := range ids {
				if primary == "" || id == pawID {
					primary = id
				}
			}
			if primary != "" {
				if pu := m.PixivUsers[primary]; pu != nil {
					row.Links.Pixiv = pu.URL
				} else {
					row.Links.Pixiv = pixivBase + "/users/" + primary
				}
			}
			if paw != nil {
				row.Links.Pawchive = paw.URL
				row.Favorited = paw.Favorited
				row.Icon = pawchiveIcon(paw.Service, paw.ID)
				row.Banner = pawchiveBanner(paw.Service, paw.ID)
			} else if a.pawIdx == nil && e.prev != nil && e.prev.Links.Pawchive != "" {
				// no pawchive list this run (-no-pawchive): keep what the file has
				row.Links.Pawchive, row.Favorited = e.prev.Links.Pawchive, e.prev.Favorited
				row.Icon, row.Banner = e.prev.Icon, e.prev.Banner
			} else if primary != "" {
				row.Icon = icon(primary)
			}
			lookup := append([]string{e.Name}, e.Alias...)
			makeScenes, dl, ok := src.find(lookup...)
			row.MakeScenes = makeScenes
			if ok {
				row.Links.Download, row.IsRecommended = dl.URL, dl.Crown
			}
			rows = append(rows, row)
		}
	}

	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Favorited != rows[j].Favorited {
			return rows[i].Favorited > rows[j].Favorited
		}
		if li, lj := rows[i].Links.count(), rows[j].Links.count(); li != lj {
			return li > lj
		}
		return strings.ToLower(rows[i].Name) < strings.ToLower(rows[j].Name)
	})
	return rows
}