package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PuerkitoBio/goquery"
)

func kknowDoc(t *testing.T, page string) *goquery.Document {
	t.Helper()
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(page))
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

// A search result row the way XenForo 2 writes it.
func kknowRow(href string, ts int64, snippet string) string {
	return fmt.Sprintf(`<li class="block-row block-row--separated js-inlineModContainer" data-author="x">
<div class="contentRow"><div class="contentRow-main">
<h3 class="contentRow-title"><a href="%s">Thread title</a></h3>
<div class="contentRow-snippet">%s</div>
<div class="contentRow-minor"><ul class="listInline"><li><time class="u-dt" data-time="%d">Nov</time></li></ul></div>
</div></div></li>`, href, snippet, ts)
}

func kknowTestPost(id int, body string) string {
	return fmt.Sprintf(`<article class="message message--post" data-author="x" data-content="post-%d" id="js-post-%d"><div class="message-body"><div class="bbWrapper">%s</div></div></article>`, id, id, body)
}

func TestKknowHelpers(t *testing.T) {
	base, _ := url.Parse("https://kknow.cc/search/1/?q=x&o=date")
	page := `<html data-csrf="tok"><head><link rel="next" href="/search/1/?page=2&amp;q=x&amp;o=date"></head><body><ul>` +
		kknowRow("/threads/some-thread.77/post-901", 1700000100, "see <b>pixiv</b> &amp; more") +
		kknowRow("/threads/other-thread.5/", 1700000000, "") +
		`</ul></body></html>`
	hits := parseKknowHits(kknowDoc(t, page), base)
	if len(hits) != 2 || hits[0].Key != "post-901" || hits[1].Key != "thread-5" {
		t.Fatalf("hits = %+v", hits)
	}
	if hits[0].URL != "https://kknow.cc/threads/some-thread.77/post-901" || hits[0].Time.Unix() != 1700000100 {
		t.Errorf("hit = %+v", hits[0])
	}
	if !strings.Contains(hits[0].Snippet, "pixiv") || strings.Contains(hits[0].Snippet, "<b>") || !strings.Contains(hits[0].Snippet, "&") {
		t.Errorf("snippet = %q", hits[0].Snippet)
	}
	if n := nextKknowPage(kknowDoc(t, page), base); n != "https://kknow.cc/search/1/?page=2&q=x&o=date" {
		t.Errorf("next = %q", n)
	}

	thread := kknowTestPost(11, "no link") + kknowTestPost(12, `<a href="https://www.pixiv.net/en/users/123">me</a> and pixiv.net/users/456`) +
		kknowTestPost(13, `<blockquote class="bbCodeBlock bbCodeBlock--quote">quoting https://www.pixiv.net/users/999</blockquote>mine: https://www.pixiv.net/users/321`)
	posts := parseKknowPosts(kknowDoc(t, thread))
	if len(posts) != 3 || posts[0].Key != "post-11" || posts[1].Key != "post-12" || posts[2].Key != "post-13" {
		t.Fatalf("posts = %+v", posts)
	}
	ids := func(p kknowPost) []string { return kknowPixivUsers(strings.Join(p.Cands, "\n")) }
	if got := ids(posts[0]); len(got) != 0 {
		t.Errorf("post 11 ids = %v", got)
	}
	if got := ids(posts[1]); len(got) != 2 || got[0] != "123" || got[1] != "456" {
		t.Errorf("post 12 ids = %v", got)
	}
	if got := ids(posts[2]); len(got) != 1 || got[0] != "321" {
		t.Errorf("a link in a quote must be ignored, post 13 ids = %v", got)
	}
	// a link in a redirect URL, and an old-style member link
	if ids := kknowPixivUsers(`href="https://x.com/?u=https%3A%2F%2Fwww.pixiv.net%2Fusers%2F789" https://www.pixiv.net/member.php?id=55`); len(ids) != 2 {
		t.Errorf("ids = %v", ids)
	}

	site, q, o, err := parseKknowSearchURL("https://kknow.cc/search/4687554/?q=https%3A%2F%2Fwww.pixiv.net%2Fusers&o=date")
	if err != nil || site.String() != "https://kknow.cc" || q != "https://www.pixiv.net/users" || o != "date" {
		t.Errorf("%v %q %q %v", site, q, o, err)
	}
	if _, _, _, err := parseKknowSearchURL("https://kknow.cc/search/"); err == nil {
		t.Error("a search URL without q must be an error")
	}
}

func newKknowTestServer(t *testing.T, posted *[]url.Values, opened *[]string) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/search/" && r.Method == http.MethodGet:
			fmt.Fprint(w, `<html data-csrf="TOKEN"><body>form</body></html>`)
		case r.URL.Path == "/search/search" && r.Method == http.MethodPost:
			_ = r.ParseForm()
			*posted = append(*posted, r.PostForm)
			http.Redirect(w, r, "/search/99/?q=x&o=date", http.StatusSeeOther)
		case r.URL.Path == "/search/99/":
			if r.URL.Query().Get("page") == "2" {
				fmt.Fprint(w, `<html><body><ul>`+kknowRow("/threads/t.1/post-300", 1700000000, "old")+`</ul></body></html>`)
				return
			}
			fmt.Fprint(w, `<html><head><link rel="next" href="/search/99/?page=2&amp;q=x&amp;o=date"></head><body><ul>`+
				kknowRow("/threads/t.1/post-500", 1700000500, "new")+
				kknowRow("/threads/t.1/post-400", 1700000400, "mid")+
				`</ul></body></html>`)
		case strings.HasPrefix(r.URL.Path, "/threads/t.1/"):
			*opened = append(*opened, r.URL.Path)
			fmt.Fprint(w, `<html><body>`+
				kknowTestPost(500, `<a href="https://www.pixiv.net/users/500500">a</a>`)+
				kknowTestPost(400, `check https://www.pixiv.net/en/users/400400 please`)+
				kknowTestPost(300, `https://www.pixiv.net/users/300300`)+
				`</body></html>`)
		default:
			http.NotFound(w, r)
		}
	}))
	return srv
}

func TestRunKknow(t *testing.T) {
	var posted []url.Values
	var opened []string
	srv := newKknowTestServer(t, &posted, &opened)
	defer srv.Close()

	a := &App{kknow: &KknowCache{}}
	o := kknowOptions{SearchURL: srv.URL + "/search/7/?q=https%3A%2F%2Fwww.pixiv.net%2Fusers&o=date", MaxPages: 5, Delay: time.Millisecond}
	if err := a.runKknow(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if len(posted) != 1 || posted[0].Get("keywords") != "https://www.pixiv.net/users" || posted[0].Get("order") != "date" || posted[0].Get("_xfToken") != "TOKEN" {
		t.Errorf("search form = %v", posted)
	}
	for _, id := range []string{"500500", "400400", "300300"} {
		if _, ok := a.kknow.Users[id]; !ok {
			t.Errorf("account %s not found: %v", id, a.kknow.Users)
		}
	}
	if len(opened) != 1 { // all three posts are on the same page, which is opened once
		t.Errorf("opened %v", opened)
	}
	if a.kknow.NewestPost == nil || a.kknow.NewestPost.ID != "post-500" {
		t.Errorf("newest = %+v", a.kknow.NewestPost)
	}

	// Next run: nothing newer than post-500, so no thread page is opened.
	opened = nil
	if err := a.runKknow(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if len(opened) != 0 {
		t.Errorf("opened %v although nothing is new", opened)
	}
	// -full reads everything again but adds nothing twice.
	o.Full = true
	before := len(a.kknow.Users)
	if err := a.runKknow(context.Background(), o); err != nil || len(a.kknow.Users) != before {
		t.Errorf("full run: %v, %d accounts (was %d)", err, len(a.kknow.Users), before)
	}
}

func TestKknowCacheAndProfileAccounts(t *testing.T) {
	a := &App{
		mapping:   &Mapping{},
		kknow:     &KknowCache{Users: map[string]kknowUser{"900": {Post: "p"}}},
		kknowPath: filepath.Join(t.TempDir(), "pixiv", "kknow.json"),
	}
	a.mapping.init()
	a.mapping.PixivUsers["100"] = &PixivUser{ID: "100"}

	ids := a.profileAccounts()
	if len(ids) != 2 || ids[0] != "100" || ids[1] != "900" {
		t.Errorf("profile accounts = %v", ids)
	}
	if core := a.corePixivAccounts(); !core["100"] || core["900"] {
		t.Errorf("core = %v", core)
	}

	if err := a.saveKknow(); err != nil {
		t.Fatal(err)
	}
	b := &KknowCache{}
	if err := loadJSON(a.kknowPath, b); err != nil || b.Users["900"].Post != "p" {
		t.Errorf("round trip: %+v %v", b, err)
	}
	if (&App{}).saveKknow() != nil {
		t.Error("an app without a kknow path must not write anything")
	}
}

func kknowTestApp(t *testing.T) *App {
	t.Helper()
	a := &App{
		mapping: &Mapping{},
		kknow: &KknowCache{Users: map[string]kknowUser{
			"123": {Post: "p1"}, // available, named
			"124": {Post: "p2"}, // unavailable
			"125": {Post: "p3"}, // already in the authors file
			"126": {Post: "p4"}, // belongs to a koikatsucards author
		}},
		profileCache: map[string]ProfileEntry{
			"123": {Available: true, Name: "Foo", Profile: true, CheckedAt: "2026-11-01T00:00:00Z",
				Links: []ProfileLink{{URL: "https://www.patreon.com/foopatreon"}}},
			"124": {Available: false, Error: "not found", Profile: true},
			"125": {Available: true, Name: "Bar", Profile: true},
			"126": {Available: true, Name: "Baz", Profile: true},
		},
	}
	a.mapping.init()
	a.mapping.PixivUsers["126"] = &PixivUser{ID: "126", URL: pixivBase + "/users/126"}
	a.fileIdx = newAuthorIndex()
	a.fileIdx.add(&fileAuthor{Name: "Someone", Links: []string{pixivBase + "/users/125"}})
	return a
}

func TestAddKknowAuthors(t *testing.T) {
	a := kknowTestApp(t)
	if n := a.addKknowAuthors(); n != 1 {
		t.Fatalf("added %d entries, want 1 (only account 123)", n)
	}
	if e, _ := a.fileIdx.find("foo"); e == nil || e.Name != "Foo" || len(e.Links) != 1 || e.Links[0] != pixivBase+"/users/123" {
		t.Errorf("entry = %+v", e)
	}
	if pp := a.mapping.PixivProfiles["123"]; pp == nil || len(pp.Links) != 1 {
		t.Errorf("profile not stored in the mapping: %+v", pp)
	}
	if a.mapping.PixivProfiles["124"] != nil || a.mapping.PixivProfiles["125"] != nil {
		t.Error("only added accounts get a profile in the mapping")
	}
	// running again adds nothing: the account has an entry now
	if n := a.addKknowAuthors(); n != 0 {
		t.Errorf("second run added %d", n)
	}
	// an author list that doesn't exist yet is created
	b := kknowTestApp(t)
	b.fileIdx = nil
	if n := b.addKknowAuthors(); n != 2 || b.fileIdx == nil || len(b.fileIdx.entries) != 2 {
		t.Errorf("without a file: added %d", n)
	}
}

func TestKknowAccountsBecomeAuthorRows(t *testing.T) {
	a := kknowTestApp(t)
	a.addKknowAuthors()
	a.pawIdx = buildIndex([]Creator{{ID: "123", Name: "Foo creator", PublicID: "foo", Service: "fanbox", Favorited: 7}})

	findRow := func(rows []AuthorRow, name string) *AuthorRow {
		for i := range rows {
			if rows[i].Name == name {
				return &rows[i]
			}
		}
		return nil
	}

	// no koikatsucards author, no bepis author: a row from the authors list entry
	rows := a.buildAuthorRows(context.Background(), nil, false)
	r := findRow(rows, "Foo")
	if r == nil {
		t.Fatalf("no row for Foo in %+v", rows)
	}
	if r.Links.Pixiv != pixivBase+"/users/123" || r.Links.Koikatsucards != "" {
		t.Errorf("links = %+v", r.Links)
	}
	if r.Links.Pawchive != "https://pawchive.pw/fanbox/user/123" || r.Favorited != 7 {
		t.Errorf("pawchive match (fanbox id == pixiv id) missing: %+v", r)
	}

	// a bepis author with the same name: one row with both, not two
	a.mapping.BepisAuthors["Foo"] = &BepisAuthor{Name: "Foo", URL: bepisAuthorURL("Foo"), CardCount: 4}
	rows = a.buildAuthorRows(context.Background(), nil, false)
	n := 0
	for _, row := range rows {
		if row.Name == "Foo" {
			n++
		}
	}
	r = findRow(rows, "Foo")
	if n != 1 || r == nil {
		t.Fatalf("expected one row for Foo, got %d", n)
	}
	if r.Links.Bepis != bepisAuthorURL("Foo") || r.Links.Pixiv != pixivBase+"/users/123" {
		t.Errorf("bepis/pixiv links = %+v", r.Links)
	}
	if r.Links.Pawchive != "https://pawchive.pw/fanbox/user/123" {
		t.Errorf("a row made from a bepis author must also get the pawchive match of its pixiv account: %+v", r.Links)
	}

	// telegram hashtags by name
	a.telegram = &telegramTags{byNorm: map[string]string{normName("Foo"): "Foo"}, order: []string{normName("Foo")}, overrides: map[string][]string{}}
	rows = a.buildAuthorRows(context.Background(), nil, false)
	if r = findRow(rows, "Foo"); r == nil || len(r.Telegram) != 1 || r.Telegram[0] != "#Foo" {
		t.Errorf("telegram = %+v", r)
	}
}
