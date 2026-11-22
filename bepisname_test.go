package main

import (
	"context"
	"testing"
)

// A bepis-only author takes the name of the authors.json entry it belongs to.
func TestBepisOnlyRowTakesEntryName(t *testing.T) {
	a := newTestApp(t)
	a.fileIdx = &authorIndex{byName: map[string][]*fileAuthor{}, byAlias: map[string][]*fileAuthor{}, byExact: map[string][]*fileAuthor{}}
	e := &fileAuthor{Name: "Hotline Istalo", Alias: []string{"neptune", "Istalo"}}
	a.fileIdx.entries = []*fileAuthor{e}
	a.fileIdx.byName["hotlineistalo"] = []*fileAuthor{e}
	a.fileIdx.byAlias["neptune"] = []*fileAuthor{e}
	a.fileIdx.byAlias["istalo"] = []*fileAuthor{e}
	a.mapping.BepisAuthors["neptune"] = &BepisAuthor{Name: "neptune", CardCount: 5}
	rows := a.buildAuthorRows(context.Background(), nil, false)
	if len(rows) != 1 || rows[0].Name != "Hotline Istalo" || len(rows[0].Alias) != 2 || rows[0].Alias[0] != "neptune" {
		t.Fatalf("%+v", rows)
	}
	if rows[0].Links.Bepis != "https://db.bepis.moe/koikatsu?cardAuthor=neptune" {
		t.Fatalf("%q", rows[0].Links.Bepis)
	}
}
