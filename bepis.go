package main

import (
	"context"
	"log"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const bepisBase = "https://db.bepis.moe/koikatsu"

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

// bepisOptions are the settings of the bepis step (see the -bepis-* flags).
type bepisOptions struct {
	CardsPath   string // cache/bepis/cards.json, written by fetchBepisCards
	RetryFailed bool
	MaxCards    int
	Workers     int
	SaveEvery   int
	RPS         float64
}

// fetchBepisCards brings cards.json up to date with db.bepis.moe and returns every cached card.
// The site being unreachable is not fatal: the cards already cached are still returned,
// together with the error.
func fetchBepisCards(ctx context.Context, o bepisOptions) (map[int]bepisRecord, error) {
	records, err := loadBepisCards(o.CardsPath)
	if err != nil {
		return nil, err
	}
	c := &bepisClient{
		http: &http.Client{Timeout: 30 * time.Second},
		lim:  &bepisLimiter{interval: time.Duration(float64(time.Second) / max(o.RPS, 0.1))},
		base: bepisSearchBase,
	}
	err = c.fetchNew(ctx, o.CardsPath, records, bepisFetchOptions{
		retryFailed: o.RetryFailed,
		maxCards:    o.MaxCards,
		workers:     max(1, o.Workers),
		saveEvery:   max(1, o.SaveEvery),
	})
	return records, err
}

func (a *App) runBepis(ctx context.Context, o bepisOptions) error {
	records, err := fetchBepisCards(ctx, o)
	if err != nil {
		if records == nil {
			return err
		}
		// Still match with the cards we have.
		log.Printf("warning: bepis cards could not be fetched completely: %v", err)
	}
	authors := bepisCardAuthors(records)
	if len(authors) == 0 {
		log.Printf("warning: no bepis card has author data (%d cards in %s); skipping bepis matching", len(records), filepath.ToSlash(o.CardsPath))
		a.mapping.BepisAuthors = map[string]*BepisAuthor{}
		return nil
	}
	matched := a.mapping.applyBepis(authors, a.fileIdx)
	log.Printf("bepis: %d authors from %d cards; matched %d/%d koikatsucards authors",
		len(authors), len(records), matched, len(a.mapping.Authors))
	return nil
}
