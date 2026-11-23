package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	pawchiveBase        = "https://pawchive.pw"
	pawchiveCreatorsAPI = pawchiveBase + "/api/v1/creators"
)

// Creator is one entry of https://pawchive.pw/api/v1/creators.
type Creator struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	PublicID  string `json:"public_id"`
	Service   string `json:"service"`
	Favorited int    `json:"favorited"`
}

func (c *Creator) key() string { return c.Service + "/" + c.ID }
func (c *Creator) url() string { return pawchiveBase + "/" + c.Service + "/user/" + c.ID }

// PawchiveCandidate is one pawchive creator that was matched to an author / pixiv account.
type PawchiveCandidate struct {
	Service   string `json:"service"`
	ID        string `json:"id"`
	URL       string `json:"url"`
	Name      string `json:"name"`
	Favorited int    `json:"favorited"`
	Match     string `json:"match"` // "id" (fanbox id == pixiv user id), "link" (fanbox/patreon link in authors.json or on the pixiv profile), "name" or "override" (pawchive_overrides.json)
}

// PawchiveMatch is the chosen creator (highest favorited) plus every candidate considered.
type PawchiveMatch struct {
	Service    string              `json:"service"`
	ID         string              `json:"id"`
	URL        string              `json:"url"`
	Name       string              `json:"name"`
	Favorited  int                 `json:"favorited"`
	Match      string              `json:"match"`
	Ambiguous  bool                `json:"ambiguous,omitempty"` // chosen via name and several same-service creators shared that name
	Candidates []PawchiveCandidate `json:"candidates"`
}

// PawchiveUser is the reverse lookup: any pawchive creator URL -> koikatsucards authors / pixiv accounts.
type PawchiveUser struct {
	Service            string   `json:"service"`
	ID                 string   `json:"id"`
	Name               string   `json:"name"`
	Favorited          int      `json:"favorited"`
	KoikatsuAuthorURLs []string `json:"koikatsu_author_urls"`
	PixivUserURLs      []string `json:"pixiv_user_urls"`
}

// ---------------------------------------------------------------------------
// Loading creators (cached download, or a local file)
// ---------------------------------------------------------------------------

func parseCreators(b []byte) ([]Creator, error) {
	var cs []Creator
	if err := json.Unmarshal(b, &cs); err != nil {
		return nil, err
	}
	return cs, nil
}

func (a *App) loadCreators(ctx context.Context, file, cachePath string, maxAge time.Duration) ([]Creator, error) {
	if file != "" {
		b, err := os.ReadFile(file)
		if err != nil {
			return nil, err
		}
		return parseCreators(b)
	}

	st, statErr := os.Stat(cachePath)
	if statErr == nil && time.Since(st.ModTime()) < maxAge {
		log.Printf("using cached pawchive creators (%s)", cachePath)
		b, err := os.ReadFile(cachePath)
		if err != nil {
			return nil, err
		}
		return parseCreators(b)
	}

	log.Println("downloading pawchive creators (large file)...")
	big := *a
	big.http = &http.Client{Timeout: 5 * time.Minute}
	status, body, err := big.get(ctx, pawchiveCreatorsAPI, map[string]string{"Accept": "application/json"})
	var creators []Creator
	if err == nil && status != http.StatusOK {
		err = fmt.Errorf("pawchive creators: HTTP %d", status)
	}
	if err == nil {
		creators, err = parseCreators(body) // reject HTML challenge pages etc. before caching
	}
	if err == nil {
		if err = os.MkdirAll(filepath.Dir(cachePath), 0o755); err == nil {
			tmp := cachePath + ".tmp"
			if err = os.WriteFile(tmp, body, 0o644); err == nil {
				err = os.Rename(tmp, cachePath)
			}
		}
		if err == nil {
			return creators, nil
		}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if statErr == nil {
		log.Printf("warning: %v; falling back to stale cache", err)
		b, rerr := os.ReadFile(cachePath)
		if rerr != nil {
			return nil, rerr
		}
		return parseCreators(b)
	}
	return nil, fmt.Errorf("%w (download %s manually and pass -pawchive-file)", err, pawchiveCreatorsAPI)
}

// ---------------------------------------------------------------------------
// Index + matching
// ---------------------------------------------------------------------------

// normName lowercases, folds full-width ASCII, and keeps only letters and digits,
// so "多摩　丸彦" == "多摩丸彦" and "Ａlucid!" == "alucid".
func normName(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 0xFF01 && r <= 0xFF5E {
			r -= 0xFEE0
		}
		r = unicode.ToLower(r)
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

type creatorIndex struct {
	fanboxByID  map[string]*Creator
	byServiceID map[string]*Creator   // "service/id"
	byPublicID  map[string][]*Creator // "service/lowercased public_id"
	byName      map[string][]*Creator // normalized name or public_id -> creators
}

func buildIndex(creators []Creator) *creatorIndex {
	ix := &creatorIndex{
		fanboxByID:  make(map[string]*Creator),
		byServiceID: make(map[string]*Creator),
		byPublicID:  make(map[string][]*Creator),
		byName:      make(map[string][]*Creator),
	}
	for i := range creators {
		c := &creators[i]
		if c.Service == "fanbox" {
			ix.fanboxByID[c.ID] = c
		}
		ix.byServiceID[c.Service+"/"+c.ID] = c
		if c.PublicID != "" {
			k := c.Service + "/" + strings.ToLower(c.PublicID)
			ix.byPublicID[k] = append(ix.byPublicID[k], c)
		}
		seen := map[string]bool{}
		for _, n := range []string{c.Name, c.PublicID} {
			norm := normName(n)
			if utf8.RuneCountInString(norm) < 2 || seen[norm] {
				continue
			}
			seen[norm] = true
			ix.byName[norm] = append(ix.byName[norm], c)
		}
	}
	return ix
}

type cand struct {
	c     *Creator
	match string
}

// resolve finds pawchive creators for pixiv user ids, fanbox/patreon handles and names.
//
//  1. "id":   fanbox creators whose id equals a pixiv user id.
//  2. "link": creators referenced by a fanbox/patreon link (handles), by public_id or id.
//  3. "name": creators whose name or public_id equals one of the names. The names also
//     include the name and public_id of every id/link match, which is how a patreon page
//     gets linked to a fanbox match and vice versa.
//
// An id or link match is authoritative for its service, so name matches in that service
// are ignored. The candidate with the highest favorited count wins; ties prefer
// id, then link, then name.
func (ix *creatorIndex) resolve(ids, names []string, handles []Handle) *PawchiveMatch {
	found := map[string]*cand{}
	authoritative := map[string]bool{}
	all := append([]string{}, names...)

	add := func(c *Creator, match string) {
		if _, ok := found[c.key()]; ok {
			return
		}
		found[c.key()] = &cand{c, match}
		authoritative[c.Service] = true
		all = append(all, c.Name, c.PublicID)
	}

	for _, id := range ids {
		if c := ix.fanboxByID[id]; c != nil {
			add(c, "id")
		}
	}
	for _, h := range handles {
		if h.IsID {
			if c := ix.byServiceID[h.Service+"/"+h.Value]; c != nil {
				add(c, "link")
			}
			continue
		}
		for _, c := range ix.byPublicID[h.Service+"/"+h.Value] {
			add(c, "link")
		}
	}

	nameCount := map[string]int{} // service -> number of name-matched creators
	for _, n := range all {
		norm := normName(n)
		if utf8.RuneCountInString(norm) < 2 {
			continue
		}
		for _, c := range ix.byName[norm] {
			if authoritative[c.Service] {
				continue
			}
			if _, ok := found[c.key()]; ok {
				continue
			}
			found[c.key()] = &cand{c, "name"}
			nameCount[c.Service]++
		}
	}
	if len(found) == 0 {
		return nil
	}

	rank := map[string]int{"id": 0, "link": 1, "name": 2}
	list := make([]*cand, 0, len(found))
	for _, c := range found {
		list = append(list, c)
	}
	sort.Slice(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if a.c.Favorited != b.c.Favorited {
			return a.c.Favorited > b.c.Favorited
		}
		if a.match != b.match {
			return rank[a.match] < rank[b.match]
		}
		if a.c.Service != b.c.Service {
			return a.c.Service < b.c.Service
		}
		return a.c.ID < b.c.ID
	})

	best := list[0]
	res := &PawchiveMatch{
		Service:   best.c.Service,
		ID:        best.c.ID,
		URL:       best.c.url(),
		Name:      best.c.Name,
		Favorited: best.c.Favorited,
		Match:     best.match,
		Ambiguous: best.match == "name" && nameCount[best.c.Service] > 1,
	}
	for _, c := range list {
		res.Candidates = append(res.Candidates, PawchiveCandidate{
			Service: c.c.Service, ID: c.c.ID, URL: c.c.url(),
			Name: c.c.Name, Favorited: c.c.Favorited, Match: c.match,
		})
	}
	return res
}

// ---------------------------------------------------------------------------
// Apply to the mapping
// ---------------------------------------------------------------------------

func (m *Mapping) applyPawchive(creators []Creator) (authors, users int) {
	return m.applyPawchiveWith(creators, nil, nil)
}

// applyPawchiveWith is applyPawchive with the manual overrides of pawchive_overrides.json (by
// authors.json row name; fileIdx tells the row name of an author and the accounts of an
// entry). An override replaces the automatic match of the authors and pixiv accounts of its
// row, or removes it, so mapping.json, the posts and the lookup file agree with authors.json.
func (m *Mapping) applyPawchiveWith(creators []Creator, overrides pawchiveOverrides, fileIdx *authorIndex) (authors, users int) {
	ix := buildIndex(creators)
	m.PawchiveUsers = map[string]*PawchiveUser{}
	bySlug, byID := m.pawchiveOverrideTargets(overrides, fileIdx)
	warned := map[string]bool{}
	override := func(ov pawchiveOverride, auto *PawchiveMatch) *PawchiveMatch {
		if ov.None {
			return nil
		}
		match, ok := ix.overrideMatch(ov)
		if !ok {
			if k := ov.Service + "/" + ov.ID; !warned[k] {
				warned[k] = true
				log.Printf("warning: pawchive override: no creator %s in the pawchive list; keeping the automatic match", k)
			}
			return auto
		}
		return match
	}

	reverse := func(match *PawchiveMatch) []*PawchiveUser {
		var out []*PawchiveUser
		for _, c := range match.Candidates {
			pu := m.PawchiveUsers[c.URL]
			if pu == nil {
				pu = &PawchiveUser{Service: c.Service, ID: c.ID, Name: c.Name, Favorited: c.Favorited}
				m.PawchiveUsers[c.URL] = pu
			}
			out = append(out, pu)
		}
		return out
	}

	for slug, au := range m.Authors {
		names := []string{au.Name}
		for _, id := range au.PixivUserIDs {
			if pu := m.PixivUsers[id]; pu != nil && pu.Name != "" {
				names = append(names, pu.Name)
			}
		}
		links := append(append([]string{}, au.CreatorLinks...), m.profileLinkURLs(au.PixivUserIDs)...)
		au.Pawchive = ix.resolve(au.PixivUserIDs, names, creatorHandles(links))
		if ov, ok := bySlug[slug]; ok {
			au.Pawchive = override(ov, au.Pawchive)
		}
		if au.Pawchive == nil {
			continue
		}
		authors++
		for _, r := range reverse(au.Pawchive) {
			r.KoikatsuAuthorURLs = addUnique(r.KoikatsuAuthorURLs, au.KoikatsuURL)
		}
	}

	for id, pu := range m.PixivUsers {
		names := []string{pu.Name}
		var links []string
		for _, slug := range pu.AuthorSlugs {
			if au := m.Authors[slug]; au != nil {
				names = append(names, au.Name)
				links = append(links, au.CreatorLinks...)
			}
		}
		links = append(links, m.profileLinkURLs([]string{id})...)
		pu.Pawchive = ix.resolve([]string{id}, names, creatorHandles(links))
		if ov, ok := byID[id]; ok {
			pu.Pawchive = override(ov, pu.Pawchive)
		}
		if pu.Pawchive == nil {
			continue
		}
		users++
		for _, r := range reverse(pu.Pawchive) {
			r.PixivUserURLs = addUnique(r.PixivUserURLs, pu.URL)
		}
	}

	// Posts inherit the creator of their pixiv account, falling back to their author tag.
	for _, p := range m.Posts {
		p.PawchiveURL = ""
		if pu := m.PixivUsers[p.PixivUserID]; pu != nil && pu.Pawchive != nil {
			p.PawchiveURL = pu.Pawchive.URL
		} else if au := m.Authors[p.AuthorSlug]; au != nil && au.Pawchive != nil {
			p.PawchiveURL = au.Pawchive.URL
		}
	}
	return authors, users
}

func (a *App) runPawchive(ctx context.Context, file, cachePath string, maxAge time.Duration) error {
	creators, err := a.loadCreators(ctx, file, cachePath, maxAge)
	if err != nil {
		return err
	}
	if len(creators) == 0 {
		return errors.New("pawchive creators list is empty")
	}
	a.pawIdx = buildIndex(creators) // also used for authors.json-only rows of the authors list
	authors, users := a.mapping.applyPawchiveWith(creators, a.pawOverrides, a.fileIdx)
	log.Printf("pawchive: %d creators indexed; matched %d/%d authors, %d/%d pixiv accounts",
		len(creators), authors, len(a.mapping.Authors), users, len(a.mapping.PixivUsers))
	return nil
}
