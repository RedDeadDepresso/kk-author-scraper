package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const sampleCards = `[
{"id":1,"bio":"","comments":[],"failed":false},
{"id":396108,"bio":"","authors":["kenzei"],"comments":[],"failed":false},
{"id":396109,"bio":"x","authors":["kenzei","Real Handle","kenzei"," "],"comments":["c"],"failed":true}
]`

func TestBepisCardAuthors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cards.json")
	if err := os.WriteFile(path, []byte(sampleCards), 0o644); err != nil {
		t.Fatal(err)
	}
	records, err := loadBepisCards(path)
	if err != nil || len(records) != 3 {
		t.Fatalf("%d %v", len(records), err)
	}
	authors := bepisCardAuthors(records)
	if authors["kenzei"] != 2 || authors["Real Handle"] != 1 || len(authors) != 2 {
		t.Fatalf("%v", authors)
	}
	if err := os.WriteFile(path, []byte(`{"a":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadBepisCards(path); err == nil {
		t.Fatal("expected error for non-array")
	}
}

func TestBepisAuthorURL(t *testing.T) {
	if got := bepisAuthorURL("kenzei"); got != "https://db.bepis.moe/koikatsu?cardAuthor=kenzei" {
		t.Fatal(got)
	}
	if got := bepisAuthorURL("a&b c[1]"); got != "https://db.bepis.moe/koikatsu?cardAuthor=a%26b%20c%5B1%5D" {
		t.Fatal(got)
	}
}

func TestBepisPlaceholdersSkipped(t *testing.T) {
	m := &Mapping{}
	m.init()
	m.Authors["author-anonymous"] = &Author{Name: "Anonymous", Slug: "author-anonymous", KoikatsuURL: authorURL("author-anonymous")}
	n := m.applyBepis(map[string]int{"Anonymous": 189315, "[Unknown]": 105803, "Evaan": 6116}, nil, nil)
	if n != 0 || len(m.BepisAuthors) != 1 || m.BepisAuthors["Evaan"] == nil {
		t.Fatalf("matched=%d %v", n, m.BepisAuthors)
	}
}

// The committed cache must stay loadable: kk-mod-scraper downloads and parses it.
func TestRealBepisCards(t *testing.T) {
	records, err := loadBepisCards(filepath.Join("cache", "bepis", "cards.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(records) == 0 {
		t.Skip("no cards cached yet")
	}
	authors := bepisCardAuthors(records)
	t.Logf("cards=%d authors=%d", len(records), len(authors))
}

func TestApplyBepis(t *testing.T) {
	m := &Mapping{}
	m.init()
	m.Authors["author-kenzei"] = &Author{Name: "Kenzei", Slug: "author-kenzei", KoikatsuURL: authorURL("author-kenzei"), PixivUserURLs: []string{"https://www.pixiv.net/users/1"}}
	m.Authors["author-real"] = &Author{Name: "Real", Slug: "author-real", KoikatsuURL: authorURL("author-real")}
	m.Authors["author-none"] = &Author{Name: "Nobody", Slug: "author-none", KoikatsuURL: authorURL("author-none")}

	e := &fileAuthor{Name: "Real", Alias: []string{"real_handle"}}
	ix := &authorIndex{byName: map[string][]*fileAuthor{"real": {e}}, byAlias: map[string][]*fileAuthor{"realhandle": {e}}, count: 1}

	n := m.applyBepis(map[string]int{"kenzei": 2, "Real Handle": 1, "other": 4}, ix, nil)
	if n != 2 || len(m.BepisAuthors) != 3 {
		t.Fatalf("matched=%d authors=%d", n, len(m.BepisAuthors))
	}
	if b := m.Authors["author-kenzei"].Bepis; len(b) != 1 || b[0].Name != "kenzei" || b[0].Match != "name" {
		t.Fatalf("%+v", b)
	}
	if b := m.Authors["author-real"].Bepis; len(b) != 1 || b[0].Name != "Real Handle" || b[0].Match != "alias" {
		t.Fatalf("%+v", b)
	}
	ba := m.BepisAuthors["kenzei"]
	if ba.CardCount != 2 || ba.KoikatsuAuthorURLs[0] != authorURL("author-kenzei") || ba.PixivUserURLs[0] != "https://www.pixiv.net/users/1" {
		t.Fatalf("%+v", ba)
	}
	if len(m.Authors["author-none"].Bepis) != 0 || len(m.BepisAuthors["other"].KoikatsuAuthorURLs) != 0 {
		t.Fatal("unexpected match")
	}
}

func TestBepisCacheRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "cards.json")
	in := map[int]bepisRecord{
		3: {ID: 3, Bio: "a <b> & \"c\"", Comments: []string{"x", "y\r\nz"}, Authors: []string{"kenzei"}},
		1: {ID: 1, Failed: true},
		2: {ID: 2},
	}
	if err := saveBepisCards(path, in); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 5 { // "[", three cards, "]"
		t.Fatalf("expected one card per line, got %d lines:\n%s", len(lines), raw)
	}
	if !strings.HasPrefix(lines[1], `{"id":1,`) || !strings.HasPrefix(lines[3], `{"id":3,`) {
		t.Errorf("cards not sorted by id:\n%s", raw)
	}
	if !strings.Contains(lines[2], `"comments":[]`) || !strings.Contains(lines[2], `"authors":[]`) {
		t.Errorf("nil lists must be written as []: %s", lines[2])
	}
	out, err := loadBepisCards(path)
	if err != nil {
		t.Fatal(err)
	}
	if out[3].Bio != in[3].Bio || len(out[3].Comments) != 2 || !out[1].Failed || out[3].Authors[0] != "kenzei" {
		t.Errorf("round trip changed the data: %+v", out)
	}
	if m, err := loadBepisCards(filepath.Join(t.TempDir(), "none.json")); err != nil || len(m) != 0 {
		t.Errorf("a missing cache must be empty, got %v %v", m, err)
	}
}

func TestBepisFetchNew(t *testing.T) {
	const latest = 6
	var hits []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits = append(hits, r.URL.Path+"?"+r.URL.RawQuery)
		switch {
		case strings.HasSuffix(r.URL.Path, "/search"):
			fmt.Fprintf(w, `{"type":"success","data":{"cards":[{"id":%d},{"id":%d}]}}`, latest-1, latest)
		case strings.HasSuffix(r.URL.Path, "/cardPage"):
			id := r.URL.Query().Get("id")
			if id == "5" {
				http.NotFound(w, r)
				return
			}
			fmt.Fprintf(w, `{"type":"success","data":{"card":{"cardData":{"bio":"bio %s","authorHistory":["author%s","other"]}},"comments":[{"text":"c%s"}]}}`, id, id, id)
		}
	}))
	defer srv.Close()

	c := &bepisClient{http: srv.Client(), lim: &bepisLimiter{interval: time.Millisecond}, base: srv.URL}
	path := filepath.Join(t.TempDir(), "cards.json")
	records := map[int]bepisRecord{3: {ID: 3, Bio: "cached"}} // ids 1 and 2 are "older than the cache"
	ctx := context.Background()

	if err := c.fetchNew(ctx, path, records, bepisFetchOptions{workers: 2, saveEvery: 1}); err != nil {
		t.Fatal(err)
	}
	if len(records) != 4 || records[3].Bio != "cached" {
		t.Fatalf("expected the cached card to be kept and 4..6 fetched, got %d records", len(records))
	}
	if records[4].Failed || !records[5].Failed || records[6].Failed {
		t.Errorf("failed flags: 4=%v 5=%v 6=%v", records[4].Failed, records[5].Failed, records[6].Failed)
	}
	if records[4].Bio != "bio 4" || records[4].Comments[0] != "c4" {
		t.Errorf("card 4 = %+v", records[4])
	}
	if a := records[4].Authors; len(a) != 2 || a[0] != "author4" || a[1] != "other" {
		t.Errorf("card 4 authors = %v", a)
	}
	for _, h := range hits {
		if strings.Contains(h, "id=1") || strings.Contains(h, "id=3") {
			t.Errorf("fetched a card that is cached or older: %s", h)
		}
	}
	saved, err := loadBepisCards(path)
	if err != nil || len(saved) != 4 {
		t.Fatalf("saved cache: %d records, %v", len(saved), err)
	}

	// Nothing new: no card requests, only the search for the newest id.
	hits = nil
	if err := c.fetchNew(ctx, path, records, bepisFetchOptions{workers: 2, saveEvery: 10}); err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Errorf("expected only the search request, got %v", hits)
	}

	// -bepis-retry-failed: only the failed card, and it still fails (404).
	hits = nil
	if err := c.fetchNew(ctx, path, records, bepisFetchOptions{retryFailed: true, workers: 1, saveEvery: 10}); err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || !strings.Contains(hits[0], "id=5") {
		t.Errorf("retry should only ask for card 5, got %v", hits)
	}

	// max-cards caps a run.
	records = map[int]bepisRecord{}
	if err := c.fetchNew(ctx, path, records, bepisFetchOptions{maxCards: 2, workers: 1, saveEvery: 10}); err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || records[1].ID != 1 || records[2].ID != 2 {
		t.Errorf("max-cards: %v", records)
	}
}

// An unreachable site must not lose the cards already cached: they are still returned (with
// the error), so the authors can be matched.
func TestFetchBepisCardsKeepsCacheWhenSiteIsDown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "no", 403) }))
	defer srv.Close()
	old := bepisSearchBase
	bepisSearchBase = srv.URL
	defer func() { bepisSearchBase = old }()

	path := filepath.Join(t.TempDir(), "bepis", "cards.json")
	if err := saveBepisCards(path, map[int]bepisRecord{1: {ID: 1, Authors: []string{"kenzei"}}}); err != nil {
		t.Fatal(err)
	}
	records, err := fetchBepisCards(context.Background(), bepisOptions{CardsPath: path, Workers: 1, SaveEvery: 10, RPS: 1000})
	if err == nil {
		t.Error("expected an error for the unreachable site")
	}
	if len(records) != 1 || records[1].Authors[0] != "kenzei" {
		t.Errorf("cached cards lost: %v", records)
	}
}
