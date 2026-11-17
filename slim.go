package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
)

// Slim is the small lookup file for the browser extension (data/slim.json).
// It only holds what a pixiv page needs: is this post / this account known?
//
// URL templates are included so the extension does not hard-code them:
// {id} and {slug} are used as-is, {name} must be URL-encoded by the consumer.
type Slim struct {
	Version int               `json:"version"`
	URLs    map[string]string `json:"urls"`
	// Posts: pixiv post ids that exist on koikatsucards (sorted).
	Posts []int64 `json:"posts"`
	// Authors: koikatsucards author slug -> display name (only slugs used by Users).
	Authors map[string]string `json:"authors"`
	// Users: pixiv user id -> koikatsucards author slugs and bepis author names.
	Users map[string]*SlimUser `json:"users"`
}

// SlimUser is one pixiv account.
type SlimUser struct {
	Authors []string `json:"authors"`         // koikatsucards author slugs
	Bepis   []string `json:"bepis,omitempty"` // bepis author names (cardAuthor values)
	// Pawchive is the creator with the most favorites among the account's matches.
	Pawchive *SlimPawchive `json:"pawchive,omitempty"`
}

// SlimPawchive identifies a pawchive creator; URL template "pawchive" in Slim.URLs.
type SlimPawchive struct {
	Service string `json:"service"`
	ID      string `json:"id"`
}

const slimVersion = 1

func (m *Mapping) buildSlim() *Slim {
	s := &Slim{
		Version: slimVersion,
		URLs: map[string]string{
			"post":     koikatsuBase + "/contents/pixiv-{id}",
			"author":   koikatsuBase + "/search?author={slug}",
			"bepis":    bepisBase + "?cardAuthor={name}",
			"pawchive": pawchiveBase + "/{service}/user/{id}",
		},
		Posts:   make([]int64, 0, len(m.Posts)),
		Authors: map[string]string{},
		Users:   map[string]*SlimUser{},
	}

	for id := range m.Posts {
		if n, err := strconv.ParseInt(id, 10, 64); err == nil {
			s.Posts = append(s.Posts, n)
		}
	}
	sort.Slice(s.Posts, func(i, j int) bool { return s.Posts[i] < s.Posts[j] })

	for id, pu := range m.PixivUsers {
		u := &SlimUser{Authors: []string{}}
		best := pu.Pawchive // the account's own match wins
		for _, slug := range pu.AuthorSlugs {
			au := m.Authors[slug]
			if au == nil {
				continue
			}
			u.Authors = addUnique(u.Authors, slug)
			s.Authors[slug] = au.Name
			for _, b := range au.Bepis {
				u.Bepis = addUnique(u.Bepis, b.Name)
			}
			// otherwise fall back to the strongest match among its authors
			if pu.Pawchive == nil && au.Pawchive != nil && (best == nil || au.Pawchive.Favorited > best.Favorited) {
				best = au.Pawchive
			}
		}
		if best != nil {
			u.Pawchive = &SlimPawchive{Service: best.Service, ID: best.ID}
		}
		// accounts with neither an author tag nor a pawchive match are covered by Posts only
		if len(u.Authors) > 0 || u.Pawchive != nil {
			s.Users[id] = u
		}
	}
	return s
}

// saveJSONCompact writes minified JSON atomically (the slim file is shipped to clients).
func saveJSONCompact(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
