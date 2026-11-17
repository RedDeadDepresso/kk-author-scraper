package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	bepisCardsURL = "https://github.com/RedDeadDepresso/kk-mod-scraper/raw/refs/heads/main/cache/bepis/cards.json"
	bepisBase     = "https://db.bepis.moe/koikatsu"
)

// bepisAuthorURL is the human-facing Koikatsu search page for one author name.
func bepisAuthorURL(name string) string {
	return bepisBase + "?cardAuthor=" + strings.ReplaceAll(url.QueryEscape(name), "+", "%20")
}

// bepisPlaceholders are "authors" that only mean "not credited" (normalized with
// normName); they cover hundreds of thousands of cards and must not match anyone.
var bepisPlaceholders = map[string]bool{"anonymous": true, "unknown": true}

// BepisAuthor is an author name found in bepis cards.json, with the reverse links.
type BepisAuthor struct {
	Name               string   `json:"name"`
	URL                string   `json:"url"`
	CardCount          int      `json:"card_count"`
	KoikatsuAuthorURLs []string `json:"koikatsu_author_urls"`
	PixivUserURLs      []string `json:"pixiv_user_urls"`
}

// BepisRef is a bepis author matched to a koikatsucards author.
type BepisRef struct {
	Name  string `json:"name"`
	URL   string `json:"url"`
	Match string `json:"match"` // "name" (author tag name) or "alias" (authors.json name/alias)
}

// BepisCache is cache/bepis_authors.json: the authors extracted from cards.json,
// so the 30+ MB source is only downloaded when it changed (ETag) or the cache is old.
type BepisCache struct {
	ETag      string         `json:"etag,omitempty"`
	FetchedAt string         `json:"fetched_at"`
	Cards     int            `json:"cards"`
	Authors   map[string]int `json:"authors"` // author name -> number of cards
}

// parseBepisCards streams the JSON array and counts cards per author name.
func parseBepisCards(r io.Reader) (cards int, authors map[string]int, err error) {
	authors = map[string]int{}
	dec := json.NewDecoder(r)
	tok, err := dec.Token()
	if err != nil {
		return 0, nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '[' {
		return 0, nil, errors.New("cards.json is not a JSON array")
	}
	for dec.More() {
		var c struct {
			Authors []string `json:"authors"`
		}
		if err := dec.Decode(&c); err != nil {
			return 0, nil, err
		}
		cards++
		seen := map[string]bool{}
		for _, n := range c.Authors {
			n = strings.TrimSpace(n)
			if n == "" || seen[n] {
				continue
			}
			seen[n] = true
			authors[n]++
		}
	}
	return cards, authors, nil
}

func (a *App) loadBepis(ctx context.Context, srcURL, cachePath string, maxAge time.Duration) (*BepisCache, error) {
	var cache BepisCache
	if err := loadJSON(cachePath, &cache); err != nil {
		return nil, fmt.Errorf("reading %s: %w", cachePath, err)
	}
	if t, err := time.Parse(time.RFC3339, cache.FetchedAt); err == nil && time.Since(t) < maxAge {
		log.Printf("using cached bepis authors (%s)", cachePath)
		return &cache, nil
	}

	headers := map[string]string{"Accept": "application/json"}
	if cache.ETag != "" {
		headers["If-None-Match"] = cache.ETag
	}
	log.Println("fetching bepis cards.json (large file)...")
	big := *a
	big.http = &http.Client{Timeout: 5 * time.Minute}
	status, body, err := big.get(ctx, srcURL, headers)
	if err == nil && status != http.StatusOK && status != http.StatusNotModified {
		err = fmt.Errorf("bepis cards.json: HTTP %d", status)
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if cache.FetchedAt != "" {
			log.Printf("warning: %v; using stale bepis cache", err)
			return &cache, nil
		}
		return nil, err
	}

	now := time.Now().UTC().Format(time.RFC3339)
	if status == http.StatusNotModified {
		cache.FetchedAt = now
		return &cache, saveJSON(cachePath, &cache)
	}
	cards, authors, err := parseBepisCards(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("bepis cards.json: %w", err)
	}
	cache = BepisCache{
		ETag:      big.lastHeader.Get("ETag"),
		FetchedAt: now,
		Cards:     cards,
		Authors:   authors,
	}
	return &cache, saveJSON(cachePath, &cache)
}

// applyBepis records every bepis author and links them to koikatsucards authors by
// normalized name, also trying the name and aliases of the author's authors.json entry.
func (m *Mapping) applyBepis(authors map[string]int, fileIdx *authorIndex) (matched int) {
	m.BepisAuthors = make(map[string]*BepisAuthor, len(authors))
	byNorm := map[string][]string{}
	for name, n := range authors {
		if bepisPlaceholders[normName(name)] {
			continue
		}
		m.BepisAuthors[name] = &BepisAuthor{Name: name, URL: bepisAuthorURL(name), CardCount: n}
		if k := normName(name); utf8.RuneCountInString(k) >= 2 {
			byNorm[k] = append(byNorm[k], name)
		}
	}
	for _, names := range byNorm {
		sort.Strings(names)
	}

	type cand struct{ name, match string }
	for _, au := range m.Authors {
		au.Bepis = nil
		cands := []cand{{au.Name, "name"}}
		if fileIdx != nil {
			if e, _ := fileIdx.find(au.Name); e != nil {
				cands = append(cands, cand{e.Name, "alias"})
				for _, al := range e.Alias {
					cands = append(cands, cand{al, "alias"})
				}
			}
		}
		seen := map[string]bool{}
		for _, c := range cands {
			for _, bn := range byNorm[normName(c.name)] {
				if seen[bn] {
					continue
				}
				seen[bn] = true
				ba := m.BepisAuthors[bn]
				au.Bepis = append(au.Bepis, BepisRef{Name: bn, URL: ba.URL, Match: c.match})
				ba.KoikatsuAuthorURLs = addUnique(ba.KoikatsuAuthorURLs, au.KoikatsuURL)
				for _, pu := range au.PixivUserURLs {
					ba.PixivUserURLs = addUnique(ba.PixivUserURLs, pu)
				}
			}
		}
		if len(au.Bepis) > 0 {
			matched++
		}
	}
	return matched
}

func (a *App) runBepis(ctx context.Context, srcURL, cachePath string, maxAge time.Duration) error {
	cache, err := a.loadBepis(ctx, srcURL, cachePath, maxAge)
	if err != nil {
		return err
	}
	if len(cache.Authors) == 0 {
		log.Printf("warning: bepis cards.json has %d cards but no author data (no \"authors\" field); skipping bepis matching", cache.Cards)
		a.mapping.BepisAuthors = map[string]*BepisAuthor{}
		return nil
	}
	matched := a.mapping.applyBepis(cache.Authors, a.fileIdx)
	log.Printf("bepis: %d authors from %d cards; matched %d/%d koikatsucards authors",
		len(cache.Authors), cache.Cards, matched, len(a.mapping.Authors))
	return nil
}
