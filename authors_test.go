package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Snippets copied from the real https://rentry.co/SceneArchive page.
const rentryFixture = `
<a class="external" href="http://discord.gg/2HtzMahv7w" rel="" target="_blank">discord.gg/2HtzMahv7w</a>
<a class="headerlink" href="#_1" title="Permanent link"> </a>
<p><span class="md-align md-center"><span class="underline-markdown" style="text-decoration-line:underline;"><a class="external" href="https://buzzheavier.com/xsdtxrikbgi0" rel="" target="_blank"><strong>0864</strong></a></span>┃👑<span class="underline-markdown" style="text-decoration-line:underline;"><a class="external" href="https://buzzheavier.com/a92fqd1x2mho" rel="" target="_blank"><strong>AnnaAnon</strong></a></span>┃👑<span class="underline-markdown" style="text-decoration-line:underline;"><a class="external" href="https://buzzheavier.com/m82ccz208kkx" rel="" target="_blank"><strong>BT1--Bluetimez72</strong></a></span>┃👑<span class="underline-markdown" style="text-decoration-line:underline;"><a class="external" href="https://fileditchfiles.me/beta17/8e87eb020be12647cc71/Bazett_2026.7z" rel="" target="_blank"><strong>Bazett</strong></a></span>┃<br>
👑<span class="underline-markdown" style="text-decoration-line:underline;"><a class="external" href="https://buzzheavier.com/fbsjm0gp2ytq" rel="" target="_blank"><strong>Koi_S3 ~ KoiKoiS</strong></a></span>┃<span class="underline-markdown" style="text-decoration-line:underline;"><a class="external" href="https://buzzheavier.com/ti015ejcn1an" rel="" target="_blank"><strong>Leoool--rleoool</strong></a>┃<a class="external" href="https://buzzheavier.com/39g2s7fb9a1w" rel="" target="_blank"><strong>LuminousAxis</strong></a></span>┃🤡<span class="underline-markdown" style="text-decoration-line:underline;"><a class="external" href="https://buzzheavier.com/7306lbdttmr1" rel="" target="_blank"><strong>Harada</strong></a></span>┃<span class="underline-markdown" style="text-decoration-line:underline;"><a class="external" href="https://buzzheavier.com/sbu9myvou18o" rel="" target="_blank"><del><strong>SkyArcadia</strong></del></a></span>┃👑<span class="underline-markdown" style="text-decoration-line:underline;"><a class="external" href="https://buzzheavier.com/6u6bqgr8zeh4" rel="" target="_blank"><strong>米悠ch (JP) KKD &amp; ハイネ</strong></a></span></span></p>
<a class="external" href="https://github.com/GopeedLab/gopeed" rel="" target="_blank"><strong>Gopeed</strong></a>`

const namesFixture = `{
  "AnnaAnon_2026": "AnnaAnon",
  "BT1--Bluetimez72_2026": "BT1",
  "Koi_S3--KoiKoiS_2024": "KoiKoiS",
  "Leoool--rleoool_2025": "Leoool",
  "Harada_2026": "Harada",
  "米悠ch(JP)KKD&ハイネ_2025": "米悠ch (JP) KKD",
  "Aumi ~ Canyaa_2026-08-01": "Aomi",
  "ももす_momosu": null
}`

func TestParseSceneArchive(t *testing.T) {
	links := parseSceneArchive(rentryFixture)
	got := map[string]DownloadLink{}
	for _, l := range links {
		got[l.Text] = l
	}
	if len(links) != 9 {
		t.Fatalf("expected 9 download links, got %d: %+v", len(links), links)
	}
	wantCrown := map[string]bool{
		"0864": false, "AnnaAnon": true, "BT1--Bluetimez72": true, "Bazett": true,
		"Koi_S3 ~ KoiKoiS": true, "Leoool--rleoool": false, "LuminousAxis": false,
		"Harada": false, "米悠ch (JP) KKD & ハイネ": true,
	}
	for name, crown := range wantCrown {
		l, ok := got[name]
		if !ok || l.Crown != crown {
			t.Errorf("%q: found=%v crown=%v want crown=%v", name, ok, l.Crown, crown)
		}
	}
	if _, ok := got["SkyArcadia"]; ok {
		t.Error("struck-through SkyArcadia should be skipped")
	}
	if got["AnnaAnon"].URL != "https://buzzheavier.com/a92fqd1x2mho" {
		t.Errorf("url %q", got["AnnaAnon"].URL)
	}
}

func TestParseNamesAndSources(t *testing.T) {
	ms, keys, err := parseNames([]byte(namesFixture))
	if err != nil || !ms["aomi"] || len(ms) != 7 || keys["annaanon"] != "AnnaAnon" || keys[normName("Aumi ~ Canyaa")] != "Aomi" {
		t.Fatalf("%v %v %v", ms, keys, err)
	}
	src := buildSources(parseSceneArchive(rentryFixture), ms, keys)
	// the rentry text "Koi_S3 ~ KoiKoiS" and key "Koi_S3--KoiKoiS_2024" meet through normalization,
	// and the link ends up under the authors.json name "KoiKoiS"
	m, dl, ok := src.find("KoiKoiS", "some other koikatsu name")
	if !ok || !m || !dl.Crown || dl.URL != "https://buzzheavier.com/fbsjm0gp2ytq" {
		t.Fatalf("%v %v %+v", m, ok, dl)
	}
	// "Leoool" resolves through names.json even though the rentry text is "Leoool--rleoool"
	if _, dl, ok := src.find("Leoool"); !ok || dl.URL != "https://buzzheavier.com/ti015ejcn1an" {
		t.Fatalf("%+v", dl)
	}
	// fallback: no names.json route, but the plain rentry text equals the name
	if _, dl, ok := src.find("0864"); !ok || dl.Crown {
		t.Fatalf("%+v", dl)
	}
	// "Aomi" makes scenes but has no link on this page
	if m, _, ok := src.find("Aomi"); !m || ok {
		t.Fatalf("makeScenes=%v ok=%v", m, ok)
	}
}

func authorsApp(t *testing.T) *App {
	t.Helper()
	a := &App{mapping: &Mapping{}, profileCache: map[string]ProfileEntry{}, pixivCache: map[string]PixivEntry{}} // http is nil: a real request would panic
	a.mapping.init()
	return a
}

func TestBuildAuthorRows(t *testing.T) {
	a := authorsApp(t)
	m := a.mapping
	e := &fileAuthor{Name: "AnnaAnon", Alias: []string{"Anna Anon", "TheSafeAnnaAnon", "anna-anon"}}
	onlyFile := &fileAuthor{Name: "FileOnly", Alias: []string{"file-only"}, Links: []string{"https://www.pixiv.net/users/999"}}
	sharedPixiv := &fileAuthor{Name: "OtherName", Links: []string{"https://www.pixiv.net/users/555"}} // same account as Plain
	a.fileIdx = &authorIndex{byName: map[string][]*fileAuthor{"annaanon": {e}, "fileonly": {onlyFile}, "othername": {sharedPixiv}}, byAlias: map[string][]*fileAuthor{}, entries: []*fileAuthor{e, onlyFile, sharedPixiv}, count: 3}

	// AnnaAnon: pawchive + download + recommended + series
	m.Authors["author-annaanon"] = &Author{
		Name: "AnnaAnon", Slug: "author-annaanon", KoikatsuURL: authorURL("author-annaanon"),
		PixivUserIDs: []string{"111", "37736420"},
		Pawchive:     &PawchiveMatch{Service: "fanbox", ID: "37736420", URL: "https://pawchive.pw/fanbox/user/37736420", Favorited: 45595},
		Bepis:        []BepisRef{{Name: "annaanon", Match: "name"}, {Name: "AnnaAnon", Match: "name"}},
	}
	m.PixivUsers["111"] = &PixivUser{ID: "111", URL: pixivBase + "/users/111"}
	m.PixivUsers["37736420"] = &PixivUser{ID: "37736420", URL: pixivBase + "/users/37736420"}
	m.BepisAuthors["annaanon"] = &BepisAuthor{Name: "annaanon", URL: bepisAuthorURL("annaanon"), CardCount: 3, KoikatsuAuthorURLs: []string{"x"}}
	m.BepisAuthors["AnnaAnon"] = &BepisAuthor{Name: "AnnaAnon", URL: bepisAuthorURL("AnnaAnon"), CardCount: 9, KoikatsuAuthorURLs: []string{"x"}}
	for i, w := range []string{"Uma Musume", "Uma Musume", "Blue Archive", "Unknown work", "", "Original", "Unknown", " original "} {
		id := string(rune('a' + i))
		m.Posts[id] = &Post{PixivID: id, AuthorSlug: "author-annaanon", WorkName: w, PixivUserID: "111"}
	}

	// Plain: no pawchive -> pixiv icon from the profile cache, empty banner
	m.Authors["author-plain"] = &Author{Name: "Plain", Slug: "author-plain", KoikatsuURL: authorURL("author-plain"), PixivUserIDs: []string{"555"}}
	m.PixivUsers["555"] = &PixivUser{ID: "555", URL: pixivBase + "/users/555"}
	a.profileCache["555"] = ProfileEntry{Available: true, Image: "https://i.pximg.net/user-profile/img/plain.png"}

	// Nobody: nothing at all
	m.Authors["author-nobody"] = &Author{Name: "Nobody", Slug: "author-nobody", KoikatsuURL: authorURL("author-nobody")}

	// bepis-only authors (case variants merge)
	m.BepisAuthors["Solo"] = &BepisAuthor{Name: "Solo", URL: bepisAuthorURL("Solo"), CardCount: 5}
	m.BepisAuthors["solo"] = &BepisAuthor{Name: "solo", URL: bepisAuthorURL("solo"), CardCount: 1}

	ms, keys, _ := parseNames([]byte(namesFixture))
	src := buildSources(parseSceneArchive(rentryFixture), ms, keys)

	rows := a.buildAuthorRows(context.Background(), src, false)
	by := map[string]AuthorRow{}
	for _, r := range rows {
		by[r.Name] = r
	}
	fo, ok := by["FileOnly"]
	if !ok || fo.Links.Pixiv != "https://www.pixiv.net/users/999" || strings.Join(fo.Alias, ",") != "file-only" || fo.Links.Koikatsucards != "" {
		t.Fatalf("authors.json-only row: %+v", fo)
	}
	if _, dup := by["OtherName"]; dup {
		t.Fatal("entry sharing a pixiv account with an existing author must not get its own row")
	}
	if len(rows) != 5 || rows[0].Name != "AnnaAnon" {
		t.Fatalf("rows=%d first=%q", len(rows), rows[0].Name)
	}

	anna := by["AnnaAnon"]
	wantLinks := AuthorLinks{
		Pixiv:         "https://www.pixiv.net/users/37736420", // matches the pawchive fanbox id, not the account with more posts
		Download:      "https://buzzheavier.com/a92fqd1x2mho",
		Pawchive:      "https://pawchive.pw/fanbox/user/37736420",
		Bepis:         "https://db.bepis.moe/koikatsu?cardAuthor=AnnaAnon", // most cards
		Koikatsucards: "https://koikatsucards.com/search?author=author-annaanon",
	}
	if anna.Links != wantLinks || !anna.IsRecommended || anna.Favorited != 45595 {
		t.Fatalf("%+v", anna)
	}
	if anna.Icon != "https://pawchive.pw/icons/fanbox/37736420" || anna.Banner != "https://pawchive.pw/banners/fanbox/37736420" {
		t.Fatalf("%q %q", anna.Icon, anna.Banner)
	}
	if strings.Join(anna.Alias, ",") != "Anna Anon,TheSafeAnnaAnon,anna-anon" { // the koikatsucards name and bepis case variants equal the name and are dropped
		t.Fatalf("alias %v", anna.Alias)
	}
	if strings.Join(anna.Series, ",") != "Uma Musume,Blue Archive" || !anna.MakeScenes {
		t.Fatalf("series %v makeScenes=%v", anna.Series, anna.MakeScenes)
	}

	plain := by["Plain"]
	if plain.Icon != "https://i.pximg.net/user-profile/img/plain.png" || plain.Banner != "" || plain.Links.Pawchive != "" || plain.Links.Pixiv != "https://www.pixiv.net/users/555" {
		t.Fatalf("%+v", plain)
	}
	nobody := by["Nobody"]
	if nobody.Icon != "" || nobody.Links.Pixiv != "" || nobody.Series == nil || nobody.Alias == nil {
		t.Fatalf("%+v", nobody)
	}
	solo := by["Solo"]
	if solo.Links.Bepis != "https://db.bepis.moe/koikatsu?cardAuthor=Solo" || solo.Links.Koikatsucards != "" || len(solo.Alias) != 0 { // "solo" is a case variant of "Solo"
		t.Fatalf("%+v", solo)
	}

	// JSON shape: every key present, empty values are "" / [] and never null
	b, _ := json.Marshal(nobody)
	for _, want := range []string{`"links":{"pixiv":"","download":"","pawchive":"","bepis":"","koikatsucards":`, `"alias":[]`, `"series":[]`, `"isRecommended":false`, `"makeScenes":false`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("missing %s in %s", want, b)
		}
	}
}

func TestFetchCached(t *testing.T) {
	hits := 0
	fail := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if fail {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Write([]byte(`{"a":"b"}`))
	}))
	defer srv.Close()
	a := &App{http: srv.Client()}
	path := filepath.Join(t.TempDir(), "names.json")
	ok := func(b []byte) error { _, _, err := parseNames(b); return err }
	ctx := context.Background()

	if _, err := a.fetchCached(ctx, "x", srv.URL, path, time.Hour, ok); err != nil || hits != 1 {
		t.Fatalf("%v hits=%d", err, hits)
	}
	if _, err := a.fetchCached(ctx, "x", srv.URL, path, time.Hour, ok); err != nil || hits != 1 {
		t.Fatalf("fresh cache must not hit the network: %v hits=%d", err, hits)
	}
	fail = true
	os.Chtimes(path, time.Now().Add(-48*time.Hour), time.Now().Add(-48*time.Hour))
	b, err := a.fetchCached(ctx, "x", srv.URL, path, time.Hour, ok)
	if err != nil || string(b) != `{"a":"b"}` {
		t.Fatalf("stale fallback: %v %s", err, b)
	}
}

func TestBuildAuthorRowsKeepsFileAliasesAndMergesPlaceholderNames(t *testing.T) {
	a := authorsApp(t)
	m := a.mapping
	// the file knows "Yata'Ryhna" (bepis name) under another spelling and an alias
	e := &fileAuthor{Name: "Yata’Ryhna", Alias: []string{"Yata_Durian"}}
	a.fileIdx = &authorIndex{
		byName: map[string][]*fileAuthor{normName("Yata’Ryhna"): {e}}, byAlias: map[string][]*fileAuthor{},
		entries: []*fileAuthor{e}, count: 1,
	}
	m.BepisAuthors["Yata'Ryhna"] = &BepisAuthor{Name: "Yata'Ryhna", URL: bepisAuthorURL("Yata'Ryhna"), CardCount: 2}

	// a placeholder name has no letters or digits, so it cannot be matched by normalized name
	m.Authors["author-unknown-1"] = &Author{Name: ".", Slug: "author-unknown-1", KoikatsuURL: authorURL("author-unknown-1")}
	m.BepisAuthors["."] = &BepisAuthor{Name: ".", URL: bepisAuthorURL("."), CardCount: 1}

	rows := a.buildAuthorRows(context.Background(), nil, false)
	by := map[string][]AuthorRow{}
	for _, r := range rows {
		by[r.Name] = append(by[r.Name], r)
	}
	if y := by["Yata'Ryhna"]; len(y) != 1 || strings.Join(y[0].Alias, ",") != "Yata’Ryhna,Yata_Durian" {
		t.Fatalf("%+v", y)
	}
	dot := by["."]
	if len(dot) != 1 || dot[0].Links.Koikatsucards == "" || dot[0].Links.Bepis != bepisAuthorURL(".") {
		t.Fatalf("placeholder-name duplicates must merge into one row: %+v", dot)
	}
}
