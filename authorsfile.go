package main

import (
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"regexp"
	"strings"
)

// fileAuthor is one entry of authors.json.
type fileAuthor struct {
	Name  string   `json:"name"`
	Alias []string `json:"alias"`
	ID    string   `json:"id"`
	Links []string `json:"links"`
}

// authorIndex looks up authors.json entries by normalized name or alias.
type authorIndex struct {
	byName  map[string][]*fileAuthor
	byAlias map[string][]*fileAuthor
	count   int
}

func loadAuthorsFile(path string) (*authorIndex, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f struct {
		Authors []fileAuthor `json:"authors"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	if len(f.Authors) == 0 {
		return nil, errors.New("no authors in file")
	}
	ix := &authorIndex{
		byName:  map[string][]*fileAuthor{},
		byAlias: map[string][]*fileAuthor{},
		count:   len(f.Authors),
	}
	for i := range f.Authors {
		e := &f.Authors[i]
		nameKey := normName(e.Name)
		if nameKey != "" {
			ix.byName[nameKey] = append(ix.byName[nameKey], e)
		}
		seen := map[string]bool{nameKey: true}
		for _, al := range e.Alias {
			k := normName(al)
			if k == "" || seen[k] {
				continue
			}
			seen[k] = true
			ix.byAlias[k] = append(ix.byAlias[k], e)
		}
	}
	return ix, nil
}

// find returns the entry for a koikatsucards author name. A primary-name match
// beats an alias match. If the best tier has several entries the result is
// ambiguous and entry is nil, so callers fall back to the pixiv API.
func (ix *authorIndex) find(name string) (entry *fileAuthor, ambiguous bool) {
	key := normName(name)
	if key == "" {
		return nil, false
	}
	for _, tier := range []map[string][]*fileAuthor{ix.byName, ix.byAlias} {
		switch matches := tier[key]; len(matches) {
		case 0:
			continue
		case 1:
			return matches[0], false
		default:
			return nil, true
		}
	}
	return nil, false
}

var (
	pixivUserRe    = regexp.MustCompile(`pixiv\.net/(?:[a-z]{2}/)?users/(\d+)|pixiv\.net/member\.php\?id=(\d+)`)
	pixivArtworkRe = regexp.MustCompile(`pixiv\.net/(?:[a-z]{2}/)?artworks/(\d+)`)
)

// pixivRefs extracts pixiv account ids and artwork ids from a list of links.
// Artwork ids still need a pixiv API call to learn the account.
func pixivRefs(links []string) (users, artworks []string) {
	for _, l := range links {
		if m := pixivUserRe.FindStringSubmatch(l); m != nil {
			id := m[1]
			if id == "" {
				id = m[2]
			}
			users = addUnique(users, id)
		} else if m := pixivArtworkRe.FindStringSubmatch(l); m != nil {
			artworks = addUnique(artworks, m[1])
		}
	}
	return users, artworks
}

// Handle identifies a pawchive creator referenced by a fanbox / patreon link.
type Handle struct {
	Service string // "fanbox" or "patreon"
	IsID    bool   // true: Value is the numeric creator id; false: Value is the public_id (lowercase)
	Value   string
}

var patreonReserved = map[string]bool{}

func init() {
	for _, w := range []string{"home", "login", "signup", "posts", "about", "bepatron", "checkout", "collections",
		"explore", "apps", "policy", "user", "cw", "c", "join", "m", "pledges", "settings", "search"} {
		patreonReserved[w] = true
	}
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func pathSegments(p string) []string {
	var out []string
	for _, seg := range strings.Split(p, "/") {
		if seg != "" {
			out = append(out, seg)
		}
	}
	return out
}

// creatorHandles extracts fanbox and patreon creator references from links:
//
//	https://NAME.fanbox.cc/, https://fanbox.cc/@NAME      -> fanbox public_id
//	https://www.fanbox.cc/creator/123                      -> fanbox id
//	patreon.com/NAME, /cw/NAME, /c/NAME, /join/NAME        -> patreon public_id
//	patreon.com/u123, patreon.com/user?u=123               -> patreon id
func creatorHandles(links []string) []Handle {
	var out []Handle
	for _, l := range links {
		l = strings.TrimSpace(l)
		if !strings.Contains(l, "://") {
			l = "https://" + l
		}
		u, err := url.Parse(l)
		if err != nil || u.Host == "" {
			continue
		}
		host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
		segs := pathSegments(u.Path)

		switch {
		case host == "fanbox.cc":
			if len(segs) > 0 && strings.HasPrefix(segs[0], "@") && len(segs[0]) > 1 {
				out = append(out, Handle{"fanbox", false, strings.ToLower(segs[0][1:])})
			} else if len(segs) > 1 && segs[0] == "creator" && isDigits(segs[1]) {
				out = append(out, Handle{"fanbox", true, segs[1]})
			}
		case strings.HasSuffix(host, ".fanbox.cc"):
			if sub := strings.TrimSuffix(host, ".fanbox.cc"); sub != "" && !strings.Contains(sub, ".") {
				out = append(out, Handle{"fanbox", false, sub})
			}
		case host == "patreon.com":
			name := ""
			if len(segs) > 0 {
				switch strings.ToLower(segs[0]) {
				case "user":
					if v := u.Query().Get("u"); isDigits(v) {
						out = append(out, Handle{"patreon", true, v})
					}
					continue
				case "cw", "c", "join", "m":
					if len(segs) > 1 {
						name = segs[1]
					}
				default:
					name = segs[0]
				}
			}
			name = strings.ToLower(name)
			if name == "" || patreonReserved[name] {
				continue
			}
			if len(name) > 1 && name[0] == 'u' && isDigits(name[1:]) {
				out = append(out, Handle{"patreon", true, name[1:]})
			} else {
				out = append(out, Handle{"patreon", false, name})
			}
		}
	}
	return out
}
