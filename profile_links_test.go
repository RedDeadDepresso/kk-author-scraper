package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func urlsOf(l []ProfileLink) []string {
	var out []string
	for _, x := range l {
		out = append(out, x.URL+" ["+x.Source+"]")
	}
	return out
}

func TestExtractProfileLinks(t *testing.T) {
	comment := "Fanbox https://example.fanbox.cc/ 、patreon.com/someone。\nhttps://twitter.com/me)\nfanbox.cc/@other and https://www.fanbox.cc/@other again"
	commentHTML := `Fanbox <a href="/jump.php?https%3A%2F%2Fexample.fanbox.cc%2F" target="_blank">https://example.fanbox.cc/</a><br />` +
		`<a href="/jump.php?url=https%3A%2F%2Fskeb.jp%2F%40me%3Fa%3D1%26b%3D2">skeb</a> <a href="/users/123">me</a> <a href="https://booth.pm/x?a=1&amp;b=2">booth</a> <a href="#top">x</a>`
	got := extractProfileLinks(comment, commentHTML, "https://me.example.com/", map[string]string{"twitter": "https://twitter.com/me", "instagram": "https://instagram.com/me"})
	want := []string{
		"https://example.fanbox.cc/ [comment]",
		"https://skeb.jp/@me?a=1&b=2 [comment]",
		"https://www.pixiv.net/users/123 [comment]",
		"https://booth.pm/x?a=1&b=2 [comment]",
		"https://twitter.com/me [comment]",
		"https://www.fanbox.cc/@other [comment]", // the same address without a scheme is not listed twice
		"https://patreon.com/someone [comment]",  // written without a scheme, trailing 。 removed
		"https://me.example.com/ [webpage]",
		"https://instagram.com/me [social:instagram]", // twitter was already found in the description
	}
	g := urlsOf(got)
	if len(g) != len(want) {
		t.Fatalf("%q", g)
	}
	for i := range want {
		if g[i] != want[i] {
			t.Fatalf("%d: %q != %q\n%q", i, g[i], want[i], g)
		}
	}
	handles := creatorHandles([]string{got[0].URL, got[5].URL, got[6].URL})
	if len(handles) != 3 {
		t.Fatalf("%+v", handles)
	}
}

func TestProfileFromBody(t *testing.T) {
	body := json.RawMessage(`{"name":"Alice","image":"s.png","imageBig":"b.png","comment":"my fanbox https://a.fanbox.cc/","commentHtml":"","webpage":null,"social":[]}`)
	e := profileFromBody(body, ProfileEntry{})
	if !e.Profile || !e.Available || e.Name != "Alice" || e.Image != "b.png" || len(e.Links) != 1 || e.Links[0].URL != "https://a.fanbox.cc/" {
		t.Fatalf("%+v", e)
	}
	body = json.RawMessage(`{"name":"Bob","comment":"","commentHtml":"","webpage":"https://bob.example/","social":{"twitter":{"url":"https://twitter.com/bob"}}}`)
	e = profileFromBody(body, ProfileEntry{})
	if !e.Profile || e.Available || e.Error != "no profile image" || len(e.Links) != 2 || e.Links[1].Source != "social:twitter" {
		t.Fatalf("%+v", e)
	}
}

// A fanbox link on the profile beats a creator that only shares the pixiv name.
func TestApplyPawchiveUsesProfileLinks(t *testing.T) {
	m := &Mapping{}
	m.init()
	m.PixivUsers["9"] = &PixivUser{ID: "9", URL: pixivBase + "/users/9", Name: "Fake"}
	m.PixivProfiles["9"] = &PixivProfile{Links: []ProfileLink{{URL: "https://realname.fanbox.cc/", Source: "comment"}, {URL: "https://patreon.com/patname", Source: "comment"}}}
	m.applyPawchive([]Creator{
		{ID: "111", Name: "Somebody", PublicID: "realname", Service: "fanbox", Favorited: 5},
		{ID: "333", Name: "Fake", Service: "fanbox", Favorited: 50}, // same name, wrong person
		{ID: "222", Name: "Other", PublicID: "patname", Service: "patreon", Favorited: 3},
	})
	p := m.PixivUsers["9"].Pawchive
	if p == nil || p.ID != "111" || p.Match != "link" || len(p.Candidates) != 2 {
		t.Fatalf("%+v", p)
	}
}

func TestRunPixivLinksCoversMappingAndAuthorsFile(t *testing.T) {
	a := newTestApp(t)
	now := time.Now().UTC().Format(time.RFC3339)
	a.mapping.PixivUsers["1"] = &PixivUser{ID: "1", Pawchive: &PawchiveMatch{ID: "1"}} // already has a pawchive match
	a.mapping.PixivUsers["2"] = &PixivUser{ID: "2"}
	// an account that only exists in the authors file
	e := &fileAuthor{Name: "FileOnly", Links: []string{pixivBase + "/users/3"}}
	a.fileIdx = &authorIndex{byName: map[string][]*fileAuthor{}, byAlias: map[string][]*fileAuthor{}, byExact: map[string][]*fileAuthor{}, entries: []*fileAuthor{e}}
	fan := []ProfileLink{{URL: "https://a.fanbox.cc/", Source: "comment"}}
	a.profileCache = map[string]ProfileEntry{
		"1": {Available: true, Profile: true, CheckedAt: now, Links: fan},
		"2": {Error: "not found", Profile: true, CheckedAt: now},
		"3": {Available: true, Profile: true, CheckedAt: now, Links: fan},
	}
	a.runPixivLinks(context.Background(), 24*time.Hour, 0) // a.http is nil: a request would panic
	pp := a.mapping.PixivProfiles
	if len(pp) != 3 || len(pp["1"].Links) != 1 || len(pp["3"].Links) != 1 || pp["2"].Error != "not found" || len(pp["2"].Links) != 0 {
		t.Fatalf("%+v", pp)
	}
	if got := a.mapping.profileLinkURLs([]string{"3"}); len(got) != 1 || got[0] != "https://a.fanbox.cc/" {
		t.Fatalf("%v", got)
	}
}
