package main

// Manual pawchive matches, for the authors where the automatic one is wrong. Like the telegram
// overrides, data/pawchive_overrides.json is a JSON object keyed by the exact name of a row of
// authors.json:
//
//	{
//	  "Some Author":   "https://pawchive.pw/fanbox/user/123456",
//	  "Other Author":  "patreon/987654",
//	  "Wrong Match":   ""
//	}
//
// A creator (a pawchive URL, or "<service>/<id>") replaces the automatic match, an empty value
// removes it: the row then has no pawchive link, no favorited count and no pawchive icon or
// banner (its icon is the pixiv one again). An override that names a creator the pawchive list
// does not have is ignored with a warning. A missing file is not an error.
//
// The overrides apply to authors.json (the row with that exact name) and to mapping.json (the
// koikatsucards authors of that row, the pixiv accounts of its authors and entry, and through
// them the posts and the lookup file), where the match then says "match": "override".

import (
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"os"
	"sort"
	"strings"
)

// pawchiveOverride is one entry of the file: a creator, or none at all.
type pawchiveOverride struct {
	Service, ID string
	None        bool
}

type pawchiveOverrides map[string]pawchiveOverride

// parsePawchiveRef reads "https://pawchive.pw/fanbox/user/123" or "fanbox/123".
func parsePawchiveRef(s string) (service, id string, ok bool) {
	s = strings.TrimSpace(s)
	u, err := url.Parse(s)
	if err != nil {
		return "", "", false
	}
	segs := pathSegments(u.Path)
	switch {
	case len(segs) == 3 && segs[1] == "user":
		service, id = segs[0], segs[2]
	case len(segs) == 2:
		service, id = segs[0], segs[1]
	default:
		return "", "", false
	}
	service = strings.ToLower(service)
	return service, id, service != "" && id != ""
}

func loadPawchiveOverrides(path string) (pawchiveOverrides, error) {
	out := pawchiveOverrides{}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return out, nil
	}
	raw := map[string]string{}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for name, ref := range raw {
		if strings.TrimSpace(ref) == "" {
			out[name] = pawchiveOverride{None: true}
			continue
		}
		service, id, ok := parsePawchiveRef(ref)
		if !ok {
			return nil, fmt.Errorf("%s: %q is not a pawchive creator (use a pawchive URL or \"service/id\", or \"\" for none)", path, ref)
		}
		out[name] = pawchiveOverride{Service: service, ID: id}
	}
	return out, nil
}

// overrideMatch is the pawchive match an override stands for; ok is false when the creator is
// not in the pawchive list.
func (ix *creatorIndex) overrideMatch(ov pawchiveOverride) (*PawchiveMatch, bool) {
	c := ix.byServiceID[ov.Service+"/"+ov.ID]
	if c == nil {
		return nil, false
	}
	return &PawchiveMatch{
		Service: c.Service, ID: c.ID, URL: c.url(), Name: c.Name, Favorited: c.Favorited, Match: "override",
		Candidates: []PawchiveCandidate{{Service: c.Service, ID: c.ID, URL: c.url(), Name: c.Name, Favorited: c.Favorited, Match: "override"}},
	}, true
}

// pawchiveOverrideTargets finds what the overrides apply to in the mapping: the koikatsucards
// authors whose authors.json row (or own) name is the key, and the pixiv accounts of those
// authors and of the authors.json entries with that name. When two overrides reach the same
// target, the first name in alphabetical order wins.
func (m *Mapping) pawchiveOverrideTargets(overrides pawchiveOverrides, fileIdx *authorIndex) (bySlug map[string]pawchiveOverride, byID map[string]pawchiveOverride) {
	bySlug, byID = map[string]pawchiveOverride{}, map[string]pawchiveOverride{}
	if len(overrides) == 0 {
		return
	}
	names := make([]string, 0, len(overrides))
	for n := range overrides {
		names = append(names, n)
	}
	sort.Strings(names)

	rowName := map[string]string{} // slug -> name of its row in authors.json
	for slug, au := range m.Authors {
		rowName[slug] = au.Name
		if fileIdx != nil {
			if e, _ := fileIdx.find(au.Name); e != nil && e.Name != "" {
				rowName[slug] = e.Name
			}
		}
	}
	slugs := make([]string, 0, len(m.Authors))
	for slug := range m.Authors {
		slugs = append(slugs, slug)
	}
	sort.Strings(slugs)

	for _, name := range names {
		ov := overrides[name]
		for _, slug := range slugs {
			au := m.Authors[slug]
			if rowName[slug] != name && au.Name != name {
				continue
			}
			if _, taken := bySlug[slug]; !taken {
				bySlug[slug] = ov
			}
			for _, id := range au.PixivUserIDs {
				if _, taken := byID[id]; !taken {
					byID[id] = ov
				}
			}
		}
		if fileIdx != nil {
			for _, e := range fileIdx.entries {
				if e.Name != name {
					continue
				}
				ids, _ := pixivRefs(e.Links)
				for _, id := range ids {
					if _, taken := byID[id]; !taken {
						byID[id] = ov
					}
				}
			}
		}
	}
	return
}

// applyPawchiveOverrides replaces the pawchive match of the rows that have an override (by exact
// name). icon gives the pixiv icon of a pixiv account id, for a row that loses its pawchive icon.
// Returns how many rows were changed.
func (a *App) applyPawchiveOverrides(rows []AuthorRow, icon func(userID string) string) int {
	n := 0
	for i := range rows {
		ov, ok := a.pawOverrides[rows[i].Name]
		if !ok {
			continue
		}
		r := &rows[i]
		if ov.None {
			r.Links.Pawchive, r.Favorited, r.Banner = "", 0, ""
			r.Icon = ""
			if r.Links.Pixiv != "" {
				r.Icon = icon(strings.TrimPrefix(r.Links.Pixiv, pixivBase+"/users/"))
			}
			n++
			continue
		}
		favorited := 0
		if a.pawIdx != nil {
			c := a.pawIdx.byServiceID[ov.Service+"/"+ov.ID]
			if c == nil {
				log.Printf("warning: pawchive override of %q: no creator %s/%s in the pawchive list; keeping the automatic match", r.Name, ov.Service, ov.ID)
				continue
			}
			favorited = c.Favorited
		}
		r.Links.Pawchive = pawchiveBase + "/" + ov.Service + "/user/" + ov.ID
		r.Favorited = favorited
		r.Icon = pawchiveIcon(ov.Service, ov.ID)
		r.Banner = pawchiveBanner(ov.Service, ov.ID)
		n++
	}
	return n
}
