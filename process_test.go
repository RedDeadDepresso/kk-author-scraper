package main

import (
	"context"
	"os"
	"testing"
)

func newTestApp(t *testing.T) *App {
	t.Helper()
	m := &Mapping{}
	m.init()
	a := &App{ // http is nil on purpose: any real request would panic
		mapping:     m,
		authorCache: map[string]TagEntry{},
		pixivCache:  map[string]PixivEntry{},
		fileMemo:    map[string]fileResult{},
	}
	if _, err := os.Stat("/mnt/user-data/uploads/authors.json"); err == nil {
		a.fileIdx, _ = loadAuthorsFile("/mnt/user-data/uploads/authors.json")
	}
	return a
}

func TestProcessUsesFileThenAPI(t *testing.T) {
	a := newTestApp(t)
	if a.fileIdx == nil {
		t.Skip("no authors.json sample")
	}
	a.authorCache["solityl"] = TagEntry{Found: true, Slug: "author-solityl", Name: "SolityL"}
	a.authorCache["をりてぃす"] = TagEntry{Found: true, Slug: "author-orites", Name: "をりてぃす"}
	// fallback author: the API result is already cached, so no network is needed
	a.pixivCache["136239538"] = PixivEntry{Available: true, UserID: "999", UserName: "Orites", WithSession: true}

	ctx := context.Background()
	if err := a.process(ctx, Card{PixivID: "144766339", Author: "SolityL"}); err != nil {
		t.Fatal(err)
	}
	p := a.mapping.Posts["144766339"]
	if p.PixivUserID != "6058416" {
		t.Fatalf("file match not used: %+v", p)
	}
	pu := a.mapping.PixivUsers["6058416"]
	if pu == nil || len(pu.Sources) != 1 || pu.Sources[0] != "authors.json" || pu.AuthorSlugs[0] != "author-solityl" {
		t.Fatalf("%+v", pu)
	}
	if au := a.mapping.Authors["author-solityl"]; len(au.PixivUserIDs) != 1 {
		t.Fatalf("%+v", au)
	}

	if err := a.process(ctx, Card{PixivID: "136239538", Author: "をりてぃす"}); err != nil {
		t.Fatal(err)
	}
	pu = a.mapping.PixivUsers["999"]
	if pu == nil || pu.Sources[0] != "pixiv_api" || pu.Name != "Orites" {
		t.Fatalf("fallback not used: %+v", pu)
	}
}

func TestProcessWork(t *testing.T) {
	a := newTestApp(t)
	a.authorCache["solityl"] = TagEntry{Found: true, Slug: "author-solityl", Name: "SolityL"}
	a.pixivCache["1"] = PixivEntry{Available: true, UserID: "9", WithSession: true}
	a.fileIdx = nil

	if err := a.process(context.Background(), Card{PixivID: "1", Author: "SolityL", Work: "Azur Lane"}); err != nil {
		t.Fatal(err)
	}
	if p := a.mapping.Posts["1"]; p.WorkName != "Azur Lane" {
		t.Fatalf("%+v", p)
	}
}
