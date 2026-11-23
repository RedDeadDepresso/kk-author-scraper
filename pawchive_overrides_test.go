package main

import (
	"context"
	"testing"
)

func TestParsePawchiveRef(t *testing.T) {
	for in, want := range map[string]string{
		"https://pawchive.pw/fanbox/user/123": "fanbox/123",
		"https://pawchive.pw/patreon/user/9/": "patreon/9",
		"Fanbox/123":                          "fanbox/123",
		" patreon/55 ":                        "patreon/55",
	} {
		s, id, ok := parsePawchiveRef(in)
		if !ok || s+"/"+id != want {
			t.Errorf("%q -> %q %q %v, want %s", in, s, id, ok, want)
		}
	}
	for _, in := range []string{"foo", "https://pawchive.pw/", "a/b/c/d"} {
		if _, _, ok := parsePawchiveRef(in); ok {
			t.Errorf("%q should not parse", in)
		}
	}
}

func TestApplyPawchiveOverrides(t *testing.T) {
	a := kknowTestApp(t)
	a.addKknowAuthors()
	a.pawIdx = buildIndex([]Creator{
		{ID: "123", Name: "Foo creator", Service: "fanbox", Favorited: 7},
		{ID: "777", Name: "Right one", Service: "patreon", Favorited: 3},
	})
	find := func(name string) *AuthorRow {
		rows := a.buildAuthorRows(context.Background(), nil, false)
		for i := range rows {
			if rows[i].Name == name {
				return &rows[i]
			}
		}
		t.Fatalf("no row %q", name)
		return nil
	}
	if r := find("Foo"); r.Links.Pawchive != "https://pawchive.pw/fanbox/user/123" {
		t.Fatalf("automatic match missing: %+v", r)
	}

	// removed
	a.pawOverrides = pawchiveOverrides{"Foo": {None: true}}
	a.profileCache["123"] = ProfileEntry{Available: true, Name: "Foo", Image: "https://img/foo.png", Profile: true}
	r := find("Foo")
	if r.Links.Pawchive != "" || r.Favorited != 0 || r.Banner != "" || r.Icon != "https://img/foo.png" {
		t.Errorf("override with no creator: %+v", r)
	}

	// replaced
	a.pawOverrides = pawchiveOverrides{"Foo": {Service: "patreon", ID: "777"}}
	r = find("Foo")
	if r.Links.Pawchive != "https://pawchive.pw/patreon/user/777" || r.Favorited != 3 || r.Icon != pawchiveIcon("patreon", "777") || r.Banner != pawchiveBanner("patreon", "777") {
		t.Errorf("override with a creator: %+v", r)
	}

	// a creator that does not exist is ignored; the name must match exactly
	a.pawOverrides = pawchiveOverrides{"Foo": {Service: "patreon", ID: "404"}, "foo": {None: true}}
	if r = find("Foo"); r.Links.Pawchive != "https://pawchive.pw/fanbox/user/123" {
		t.Errorf("unknown creator / inexact name must not change the row: %+v", r)
	}
}

// The override reaches the mapping: the author, its pixiv account, the reverse index, the
// posts and the lookup file.
func TestPawchiveOverridesInMappingAndLookup(t *testing.T) {
	creators := []Creator{
		{ID: "123", Name: "Foo", Service: "fanbox", Favorited: 7},
		{ID: "777", Name: "Right one", Service: "patreon", Favorited: 3},
	}
	newMapping := func() *Mapping {
		m := &Mapping{}
		m.init()
		m.Authors["foo"] = &Author{Name: "Foo", Slug: "foo", KoikatsuURL: authorURL("foo"), PixivUserIDs: []string{"123"}}
		m.PixivUsers["123"] = &PixivUser{ID: "123", URL: pixivBase + "/users/123", AuthorSlugs: []string{"foo"}}
		m.Posts["1"] = &Post{PixivID: "1", PixivUserID: "123", AuthorSlug: "foo"}
		return m
	}
	lookupPaw := func(m *Mapping) *LookupPawchive {
		u := m.buildLookup().Users["123"]
		if u == nil {
			t.Fatal("no lookup user 123")
		}
		return u.Pawchive
	}

	m := newMapping()
	m.applyPawchiveWith(creators, nil, nil)
	if p := lookupPaw(m); p == nil || p.ID != "123" || m.Authors["foo"].Pawchive.Match != "id" {
		t.Fatalf("automatic match missing: %+v", p)
	}

	// removed (the key is the author's own name)
	m = newMapping()
	m.applyPawchiveWith(creators, pawchiveOverrides{"Foo": {None: true}}, nil)
	if m.Authors["foo"].Pawchive != nil || m.PixivUsers["123"].Pawchive != nil || lookupPaw(m) != nil || len(m.PawchiveUsers) != 0 || m.Posts["1"].PawchiveURL != "" {
		t.Errorf("override with no creator left a match: %+v %+v %v", m.Authors["foo"].Pawchive, m.PixivUsers["123"].Pawchive, m.PawchiveUsers)
	}

	// replaced, with the key being the authors.json row name of the author
	m = newMapping()
	idx := newAuthorIndex()
	idx.add(&fileAuthor{Name: "Foo Renamed", Alias: []string{"Foo"}})
	m.applyPawchiveWith(creators, pawchiveOverrides{"Foo Renamed": {Service: "patreon", ID: "777"}}, idx)
	au := m.Authors["foo"].Pawchive
	if au == nil || au.ID != "777" || au.Match != "override" || au.Favorited != 3 || len(au.Candidates) != 1 {
		t.Fatalf("author = %+v", au)
	}
	if p := lookupPaw(m); p == nil || p.Service != "patreon" || p.ID != "777" {
		t.Errorf("lookup = %+v", p)
	}
	if m.Posts["1"].PawchiveURL != "https://pawchive.pw/patreon/user/777" {
		t.Errorf("post = %q", m.Posts["1"].PawchiveURL)
	}
	if r := m.PawchiveUsers["https://pawchive.pw/patreon/user/777"]; r == nil || len(r.KoikatsuAuthorURLs) != 1 {
		t.Errorf("reverse index = %+v", m.PawchiveUsers)
	}

	// an authors.json entry that is not a koikatsucards author: its pixiv account is overridden
	m = newMapping()
	m.Authors = map[string]*Author{}
	idx = newAuthorIndex()
	idx.add(&fileAuthor{Name: "FileOnly", Links: []string{pixivBase + "/users/123"}})
	m.applyPawchiveWith(creators, pawchiveOverrides{"FileOnly": {None: true}}, idx)
	if m.PixivUsers["123"].Pawchive != nil {
		t.Errorf("account of an authors.json entry not overridden: %+v", m.PixivUsers["123"].Pawchive)
	}

	// an unknown creator keeps the automatic match
	m = newMapping()
	m.applyPawchiveWith(creators, pawchiveOverrides{"Foo": {Service: "patreon", ID: "404"}}, nil)
	if m.Authors["foo"].Pawchive == nil || m.Authors["foo"].Pawchive.ID != "123" {
		t.Errorf("unknown creator: %+v", m.Authors["foo"].Pawchive)
	}
}
