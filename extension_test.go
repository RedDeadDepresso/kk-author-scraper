package main

import (
	"encoding/json"
	"testing"
)

func TestBuildExtension(t *testing.T) {
	m := &Mapping{}
	m.init()
	m.Posts["200"] = &Post{PixivID: "200"}
	m.Posts["50"] = &Post{PixivID: "50"}
	m.Posts["bad"] = &Post{PixivID: "bad"}
	m.Authors["author-a"] = &Author{Name: "A", Slug: "author-a", Bepis: []BepisRef{{Name: "a_bepis"}, {Name: "A"}}}
	m.Authors["author-b"] = &Author{Name: "B", Slug: "author-b"}
	m.Authors["author-unused"] = &Author{Name: "Unused", Slug: "author-unused"}
	m.PixivUsers["1"] = &PixivUser{ID: "1", AuthorSlugs: []string{"author-a", "author-b"}}
	m.PixivUsers["2"] = &PixivUser{ID: "2"}                                                       // no author tag, no pawchive: only covered by posts
	m.PixivUsers["3"] = &PixivUser{ID: "3", Pawchive: &PawchiveMatch{Service: "fanbox", ID: "3"}} // pawchive only
	m.PixivUsers["4"] = &PixivUser{ID: "4", AuthorSlugs: []string{"author-a", "author-b"}}        // falls back to authors
	m.Authors["author-a"].Pawchive = &PawchiveMatch{Service: "patreon", ID: "9", Favorited: 10}
	m.Authors["author-b"].Pawchive = &PawchiveMatch{Service: "fanbox", ID: "8", Favorited: 500}
	m.PixivUsers["1"].Pawchive = &PawchiveMatch{Service: "fanbox", ID: "1", Favorited: 1} // own match beats author's

	s := m.buildExtension()
	if len(s.Posts) != 2 || s.Posts[0] != 50 || s.Posts[1] != 200 {
		t.Fatalf("posts %v", s.Posts)
	}
	if len(s.Users) != 3 || len(s.Users["1"].Authors) != 2 || len(s.Users["1"].Bepis) != 2 {
		t.Fatalf("users %+v", s.Users)
	}
	if p := s.Users["1"].Pawchive; p == nil || p.Service != "fanbox" || p.ID != "1" {
		t.Fatalf("own match: %+v", p)
	}
	if p := s.Users["4"].Pawchive; p == nil || p.ID != "8" {
		t.Fatalf("fallback to strongest author match: %+v", p)
	}
	if u := s.Users["3"]; u.Pawchive == nil || u.Authors == nil || len(u.Authors) != 0 {
		t.Fatalf("pawchive-only: %+v", u)
	}
	if len(s.Authors) != 2 || s.Authors["author-a"] != "A" || s.Authors["author-unused"] != "" {
		t.Fatalf("authors %v", s.Authors)
	}
	b, _ := json.Marshal(s)
	var back Extension
	if err := json.Unmarshal(b, &back); err != nil || back.Version != 1 || back.URLs["bepis"] != "https://db.bepis.moe/koikatsu?cardAuthor={name}" || back.URLs["pawchive"] != "https://pawchive.pw/{service}/user/{id}" {
		t.Fatalf("%v %s", err, b)
	}
}
