package main

import (
	"context"
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

func TestParseBepisCards(t *testing.T) {
	cards, authors, err := parseBepisCards(strings.NewReader(sampleCards))
	if err != nil || cards != 3 || authors["kenzei"] != 2 || authors["Real Handle"] != 1 || len(authors) != 2 {
		t.Fatalf("%d %v %v", cards, authors, err)
	}
	if _, _, err := parseBepisCards(strings.NewReader(`{"a":1}`)); err == nil {
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
	n := m.applyBepis(map[string]int{"Anonymous": 189315, "[Unknown]": 105803, "Evaan": 6116}, nil)
	if n != 0 || len(m.BepisAuthors) != 1 || m.BepisAuthors["Evaan"] == nil {
		t.Fatalf("matched=%d %v", n, m.BepisAuthors)
	}
}

func TestRealBepisCards(t *testing.T) {
	f, err := os.Open("/mnt/user-data/uploads/cards.json")
	if err != nil {
		t.Skip("no sample")
	}
	defer f.Close()
	cards, authors, err := parseBepisCards(f)
	if err != nil || cards < 400000 || authors["Evaan"] < 5000 || len(authors) < 8000 {
		t.Fatalf("cards=%d authors=%d err=%v", cards, len(authors), err)
	}
	t.Logf("cards=%d authors=%d", cards, len(authors))
}

func TestApplyBepis(t *testing.T) {
	m := &Mapping{}
	m.init()
	m.Authors["author-kenzei"] = &Author{Name: "Kenzei", Slug: "author-kenzei", KoikatsuURL: authorURL("author-kenzei"), PixivUserURLs: []string{"https://www.pixiv.net/users/1"}}
	m.Authors["author-real"] = &Author{Name: "Real", Slug: "author-real", KoikatsuURL: authorURL("author-real")}
	m.Authors["author-none"] = &Author{Name: "Nobody", Slug: "author-none", KoikatsuURL: authorURL("author-none")}

	e := &fileAuthor{Name: "Real", Alias: []string{"real_handle"}}
	ix := &authorIndex{byName: map[string][]*fileAuthor{"real": {e}}, byAlias: map[string][]*fileAuthor{"realhandle": {e}}, count: 1}

	n := m.applyBepis(map[string]int{"kenzei": 2, "Real Handle": 1, "other": 4}, ix)
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

func TestLoadBepisETag(t *testing.T) {
	hits, conditional := 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.Header.Get("If-None-Match") == `"v1"` {
			conditional++
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		w.Write([]byte(sampleCards))
	}))
	defer srv.Close()

	a := &App{http: srv.Client(), mapping: &Mapping{}}
	path := filepath.Join(t.TempDir(), "bepis_authors.json")
	ctx := context.Background()

	c, err := a.loadBepis(ctx, srv.URL, path, time.Hour)
	if err != nil || c.ETag != `"v1"` || c.Authors["kenzei"] != 2 || hits != 1 {
		t.Fatalf("%+v %v hits=%d", c, err, hits)
	}
	// fresh cache: no request at all
	if _, err := a.loadBepis(ctx, srv.URL, path, time.Hour); err != nil || hits != 1 {
		t.Fatalf("hits=%d %v", hits, err)
	}
	// expired cache: conditional request, 304, cached data kept
	c, err = a.loadBepis(ctx, srv.URL, path, 0)
	if err != nil || conditional != 1 || c.Authors["kenzei"] != 2 {
		t.Fatalf("%+v %v cond=%d", c, err, conditional)
	}
}
