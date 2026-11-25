package main

// Fetches the cards of db.bepis.moe into cache/bepis/cards.json, so a card is only ever
// fetched once. The cache is a JSON array of {id, bio, authors, comments, failed}, one card
// per line (valid JSON, but a daily commit only adds lines instead of rewriting a 40+ MB
// line). Cards the site refused are kept with "failed": true and only tried again with
// -bepis-retry-failed.
//
// Ids go up from the last cached one to the newest card, which the site's "most recent"
// search reports. Caveat: a card is fetched once, so comments posted on it later are not seen.
//
// This file is also consumed by kk-mod-scraper, which downloads it from this repository to
// file the mod links found in the bios and comments (that is why the format must not change
// without changing that scraper too). The authors of the cards feed bepis.go.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// bepisSearchBase is a variable so that tests can point it at a fake server.
var bepisSearchBase = "https://db.bepis.moe/api/frontend"

const (
	bepisCardType  = "KK"
	bepisUserAgent = "kk-author-scraper-bepis/1.0"
)

// bepisRecord is one cached card.
type bepisRecord struct {
	ID       int      `json:"id"`
	Bio      string   `json:"bio"`
	Authors  []string `json:"authors"`
	Comments []string `json:"comments"`
	Failed   bool     `json:"failed"`
}

// ---------------------------------------------------------------------------
// API response shapes
// ---------------------------------------------------------------------------

type bepisSearchResp struct {
	Type string `json:"type"`
	Data struct {
		Cards []struct {
			ID int `json:"id"`
		} `json:"cards"`
	} `json:"data"`
}

type bepisCardResp struct {
	Type string `json:"type"`
	Data struct {
		Card struct {
			CardData struct {
				Bio           string   `json:"bio"`
				AuthorHistory []string `json:"authorHistory"`
			} `json:"cardData"`
		} `json:"card"`
		Comments []struct {
			Text string `json:"text"`
		} `json:"comments"`
	} `json:"data"`
}

// ---------------------------------------------------------------------------
// HTTP: one rate limiter shared by all workers, retries with backoff
// ---------------------------------------------------------------------------

type bepisLimiter struct {
	mu       sync.Mutex
	next     time.Time
	interval time.Duration
}

func (l *bepisLimiter) wait(ctx context.Context) error {
	l.mu.Lock()
	now := time.Now()
	if l.next.Before(now) {
		l.next = now
	}
	slot := l.next
	l.next = slot.Add(l.interval)
	l.mu.Unlock()

	select {
	case <-time.After(time.Until(slot)):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// penalize pushes every worker back (used on 429 / server errors).
func (l *bepisLimiter) penalize(d time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if t := time.Now().Add(d); t.After(l.next) {
		l.next = t
	}
}

type bepisClient struct {
	http *http.Client
	lim  *bepisLimiter
	base string
}

var errBepisPermanent = errors.New("permanent error")

func (c *bepisClient) getJSON(ctx context.Context, url string, out any) error {
	const maxAttempts = 6
	backoff := 2 * time.Second

	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := c.lim.wait(ctx); err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return fmt.Errorf("%w: %v", errBepisPermanent, err)
		}
		req.Header.Set("User-Agent", bepisUserAgent)

		resp, err := c.http.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			lastErr = err
			c.lim.penalize(backoff)
			backoff *= 2
			continue
		}
		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()

		switch {
		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
			wait := backoff
			if ra := resp.Header.Get("Retry-After"); ra != "" {
				if secs, err := strconv.Atoi(ra); err == nil {
					wait = time.Duration(secs) * time.Second
				} else if t, err := http.ParseTime(ra); err == nil {
					wait = time.Until(t)
				}
			}
			if wait < time.Second {
				wait = time.Second
			}
			log.Printf("bepis is pushing back (HTTP %d), pausing all workers for %s (attempt %d of %d)",
				resp.StatusCode, wait.Round(time.Second), attempt, maxAttempts)
			c.lim.penalize(wait)
			backoff *= 2
			lastErr = fmt.Errorf("http %d", resp.StatusCode)
			continue
		case resp.StatusCode != http.StatusOK:
			return fmt.Errorf("%w: http %d", errBepisPermanent, resp.StatusCode)
		case readErr != nil:
			lastErr = readErr
			continue
		}
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("%w: bad json: %v", errBepisPermanent, err)
		}
		return nil
	}
	return fmt.Errorf("giving up after %d attempts: %w", maxAttempts, lastErr)
}

// ---------------------------------------------------------------------------
// Card cache
// ---------------------------------------------------------------------------

// loadBepisCards reads the cache (a missing file is an empty cache).
func loadBepisCards(path string) (map[int]bepisRecord, error) {
	m := map[int]bepisRecord{}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	var list []bepisRecord
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	for _, r := range list {
		m[r.ID] = r
	}
	return m, nil
}

// saveBepisCards writes the cache atomically (temp file + rename), sorted by id, one card per
// line so that git diffs only show the cards that were added or changed.
func saveBepisCards(path string, m map[int]bepisRecord) error {
	ids := make([]int, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	sort.Ints(ids)

	var buf bytes.Buffer
	buf.WriteString("[\n")
	for i, id := range ids {
		r := m[id]
		if r.Comments == nil {
			r.Comments = []string{}
		}
		if r.Authors == nil {
			r.Authors = []string{}
		}
		b, err := json.Marshal(r)
		if err != nil {
			return err
		}
		buf.Write(b)
		if i < len(ids)-1 {
			buf.WriteByte(',')
		}
		buf.WriteByte('\n')
	}
	buf.WriteString("]\n")

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ---------------------------------------------------------------------------
// Fetching cards
// ---------------------------------------------------------------------------

func (c *bepisClient) latestID(ctx context.Context) (int, error) {
	var s bepisSearchResp
	url := fmt.Sprintf("%s/search?cardType=%s&orderType=mostRecent", c.base, bepisCardType)
	if err := c.getJSON(ctx, url, &s); err != nil {
		return 0, err
	}
	if s.Type != "success" || len(s.Data.Cards) == 0 {
		return 0, errors.New("the search returned no cards")
	}
	latest := 0
	for _, card := range s.Data.Cards {
		latest = max(latest, card.ID)
	}
	return latest, nil
}

func (c *bepisClient) fetchCard(ctx context.Context, id int) bepisRecord {
	rec := bepisRecord{ID: id, Authors: []string{}, Comments: []string{}}
	var cr bepisCardResp
	url := fmt.Sprintf("%s/cardPage?cardType=%s&id=%d", c.base, bepisCardType, id)
	if err := c.getJSON(ctx, url, &cr); err != nil || cr.Type != "success" {
		rec.Failed = true
		return rec
	}
	rec.Bio = cr.Data.Card.CardData.Bio
	if a := cr.Data.Card.CardData.AuthorHistory; a != nil {
		rec.Authors = a
	}
	for _, cm := range cr.Data.Comments {
		rec.Comments = append(rec.Comments, cm.Text)
	}
	return rec
}

type bepisFetchOptions struct {
	retryFailed bool
	maxCards    int // 0 = no limit
	workers     int
	saveEvery   int
}

// fetchNew fetches the cards that aren't cached yet (or, with retryFailed, the failed ones)
// and stores them in records and on disk. Interrupting (Ctrl+C) saves what was fetched so far.
func (c *bepisClient) fetchNew(ctx context.Context, cachePath string, records map[int]bepisRecord, opt bepisFetchOptions) error {
	var ids []int
	if opt.retryFailed {
		for id, r := range records {
			if r.Failed {
				ids = append(ids, id)
			}
		}
		sort.Ints(ids)
		log.Printf("retrying %d failed bepis cards", len(ids))
	} else {
		latest, err := c.latestID(ctx)
		if err != nil {
			return fmt.Errorf("could not get the newest bepis card id: %w", err)
		}
		start := 1
		for id := range records {
			start = max(start, id+1)
		}
		log.Printf("bepis cards: cached up to %d, newest %d", start-1, latest)
		for id := start; id <= latest; id++ {
			ids = append(ids, id)
		}
	}
	if opt.maxCards > 0 && len(ids) > opt.maxCards {
		log.Printf("warning: fetching only %d of %d new bepis cards this run; the rest follows next run", opt.maxCards, len(ids))
		ids = ids[:opt.maxCards]
	}
	if len(ids) == 0 {
		log.Println("no new bepis cards")
		return nil
	}
	log.Printf("fetching %d bepis cards (%d..%d)", len(ids), ids[0], ids[len(ids)-1])

	jobs := make(chan int)
	results := make(chan bepisRecord)
	var wg sync.WaitGroup
	for i := 0; i < opt.workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for id := range jobs {
				rec := c.fetchCard(ctx, id)
				if ctx.Err() != nil {
					return // interrupted: don't record a bogus failure
				}
				results <- rec
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, id := range ids {
			select {
			case jobs <- id:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		wg.Wait()
		close(results)
	}()

	done, failed, sinceSave := 0, 0, 0
	for rec := range results {
		records[rec.ID] = rec
		done++
		sinceSave++
		if rec.Failed {
			failed++
		}
		if sinceSave >= opt.saveEvery {
			if err := saveBepisCards(cachePath, records); err != nil {
				log.Printf("could not save the bepis card cache: %v", err)
			}
			sinceSave = 0
			log.Printf("bepis progress: %d of %d, %d failed, last id %d", done, len(ids), failed, rec.ID)
		}
	}
	if err := saveBepisCards(cachePath, records); err != nil {
		return fmt.Errorf("could not save the bepis card cache: %w", err)
	}
	log.Printf("bepis cards fetched: %d (%d failed), %d cached", done, failed, len(records))
	return nil
}

// bepisCardAuthors counts the cards per author name (each name once per card, blank names
// ignored) of the cached cards.
func bepisCardAuthors(records map[int]bepisRecord) map[string]int {
	authors := map[string]int{}
	for _, r := range records {
		seen := map[string]bool{}
		for _, n := range r.Authors {
			n = strings.TrimSpace(n)
			if n == "" || seen[n] {
				continue
			}
			seen[n] = true
			authors[n]++
		}
	}
	return authors
}

// bepisMinSharedCards is how many cards names must share before they count as one author:
// two names that only met on a single card could just be a collaboration.
const bepisMinSharedCards = 2

// bepisAuthorGroups finds the author names that always appear together: names that are on
// exactly the same cards (at least bepisMinSharedCards of them) are aliases of one author.
// Only groups of two or more names are returned, each sorted by name, the groups sorted by
// their first name. Blank and placeholder names ("Anonymous") never join a group.
func bepisAuthorGroups(records map[int]bepisRecord) [][]string {
	cards := map[string][]int{}
	for id, r := range records {
		seen := map[string]bool{}
		for _, n := range r.Authors {
			n = strings.TrimSpace(n)
			if n == "" || seen[n] || bepisPlaceholders[normName(n)] {
				continue
			}
			seen[n] = true
			cards[n] = append(cards[n], id)
		}
	}
	bySig := map[[sha256.Size]byte][]string{}
	for n, ids := range cards {
		if len(ids) < bepisMinSharedCards {
			continue
		}
		sort.Ints(ids)
		h := sha256.New()
		for _, id := range ids {
			h.Write([]byte(strconv.Itoa(id) + ","))
		}
		var sig [sha256.Size]byte
		copy(sig[:], h.Sum(nil))
		bySig[sig] = append(bySig[sig], n)
	}
	var groups [][]string
	for _, names := range bySig {
		if len(names) < 2 {
			continue
		}
		sort.Strings(names)
		groups = append(groups, names)
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i][0] < groups[j][0] })
	return groups
}
