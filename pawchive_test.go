package main

import (
	"os"
	"testing"
)

func TestNormName(t *testing.T) {
	if normName("多摩\u3000丸彦") != "多摩丸彦" || normName("Ａlucid!") != "alucid" {
		t.Fatal("normName")
	}
}

func TestResolve(t *testing.T) {
	cs := []Creator{
		{ID: "100", Name: "Alucid", PublicID: "alucid", Service: "fanbox", Favorited: 50},
		{ID: "9", Name: "Alucid", PublicID: "alucid_art", Service: "patreon", Favorited: 900},
		{ID: "7", Name: "Other", PublicID: "other", Service: "fanbox", Favorited: 5000}, // same name as nobody
		{ID: "8", Name: "Solo", PublicID: "solo", Service: "patreon", Favorited: 10},
		{ID: "11", Name: "Solo", PublicID: "solo2", Service: "patreon", Favorited: 20},
	}
	ix := buildIndex(cs)

	// id match on fanbox, patreon found by the fanbox creator's name, patreon wins on favorited
	r := ix.resolve([]string{"100"}, []string{"Alucid"}, nil)
	if r == nil || r.Service != "patreon" || r.ID != "9" || len(r.Candidates) != 2 {
		t.Fatalf("%+v", r)
	}
	// fanbox wins when it has more favorites; id match shields against a same-service name match
	cs[0].Favorited = 5000
	r = ix.resolve([]string{"100"}, []string{"Alucid", "Other"}, nil)
	if r.Service != "fanbox" || r.ID != "100" || r.Match != "id" || len(r.Candidates) != 2 {
		t.Fatalf("%+v", r)
	}
	// name-only, ambiguous within a service
	r = ix.resolve([]string{"555"}, []string{"solo"}, nil)
	if r.ID != "8" && r.ID != "11" {
		t.Fatalf("%+v", r)
	}
	r = ix.resolve(nil, []string{"Solo"}, nil)
	if r == nil || r.Match != "name" {
		t.Fatalf("%+v", r)
	}
	if ix.resolve([]string{"1"}, []string{"nobody"}, nil) != nil {
		t.Fatal("expected no match")
	}
}

func TestRealCreators(t *testing.T) {
	b, err := os.ReadFile("/mnt/user-data/uploads/creators.json")
	if err != nil {
		t.Skip("no sample")
	}
	cs, err := parseCreators(b)
	if err != nil {
		t.Fatal(err)
	}
	ix := buildIndex(cs)
	t.Logf("%d creators, %d fanbox ids, %d names", len(cs), len(ix.fanboxByID), len(ix.byName))
	r := ix.resolve([]string{"1386751"}, []string{"yellowbea"}, nil)
	t.Logf("%+v", r)
	if r == nil {
		t.Fatal("expected match")
	}
}

func TestResolveByLink(t *testing.T) {
	cs := []Creator{
		{ID: "500", Name: "Hanami", PublicID: "hanamii", Service: "fanbox", Favorited: 40},
		{ID: "600", Name: "Hanami Official", PublicID: "hanamii", Service: "patreon", Favorited: 700},
		{ID: "601", Name: "Hanami", PublicID: "hanami_other", Service: "fanbox", Favorited: 9000}, // namesake
	}
	ix := buildIndex(cs)
	h := creatorHandles([]string{"https://hanamii.fanbox.cc/", "https://hanamii.gumroad.com/"})
	r := ix.resolve(nil, []string{"Hanami"}, h)
	// link match on fanbox shields the same-name fanbox namesake; patreon found via the fanbox creator's public_id
	if r == nil || r.Service != "patreon" || r.ID != "600" || len(r.Candidates) != 2 {
		t.Fatalf("%+v", r)
	}
	for _, c := range r.Candidates {
		if c.ID == "601" {
			t.Fatal("namesake should be excluded")
		}
	}
	// id handle
	r = ix.resolve(nil, nil, creatorHandles([]string{"https://www.patreon.com/u600"}))
	if r == nil || r.ID != "600" || r.Match != "link" {
		t.Fatalf("%+v", r)
	}
}

func TestRealLinkCoverage(t *testing.T) {
	cb, err1 := os.ReadFile("/mnt/user-data/uploads/creators.json")
	if err1 != nil {
		t.Skip("no samples")
	}
	creators, _ := parseCreators(cb)
	ix := buildIndex(creators)
	af, err := loadAuthorsFile("/mnt/user-data/uploads/authors.json")
	if err != nil {
		t.Skip("no authors.json")
	}
	seen := map[*fileAuthor]bool{}
	withHandles, matched, ncand := 0, 0, 0
	for _, list := range af.byName {
		for _, e := range list {
			if seen[e] {
				continue
			}
			seen[e] = true
			h := creatorHandles(e.Links)
			if len(h) == 0 {
				continue
			}
			withHandles++
			ids, _ := pixivRefs(e.Links)
			if r := ix.resolve(ids, []string{e.Name}, h); r != nil {
				matched++
				ncand += len(r.Candidates)
			}
		}
	}
	t.Logf("%d authors have fanbox/patreon links; %d resolved to a pawchive creator", withHandles, matched)
	if withHandles == 0 || matched == 0 {
		t.Fatal("expected matches")
	}
}
