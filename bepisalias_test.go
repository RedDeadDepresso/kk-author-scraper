package main

import (
	"context"
	"testing"
)

// Two bepis names of one author (one is an alias of the authors.json entry) give one row.
func TestBepisAliasJoinsEntryGroup(t *testing.T) {
	a := newTestApp(t)
	a.fileIdx = &authorIndex{byName: map[string][]*fileAuthor{}, byAlias: map[string][]*fileAuthor{}, byExact: map[string][]*fileAuthor{}}
	e := &fileAuthor{Name: "murara", Alias: []string{"musisi"}}
	a.fileIdx.entries = []*fileAuthor{e}
	a.fileIdx.byName["murara"] = []*fileAuthor{e}
	a.fileIdx.byAlias["musisi"] = []*fileAuthor{e}
	a.mapping.BepisAuthors["murara"] = &BepisAuthor{Name: "murara", CardCount: 22}
	a.mapping.BepisAuthors["musisi"] = &BepisAuthor{Name: "musisi", CardCount: 22}
	rows := a.buildAuthorRows(context.Background(), nil, false)
	if len(rows) != 1 || rows[0].Name != "murara" || len(rows[0].Alias) != 1 || rows[0].Alias[0] != "musisi" {
		t.Fatalf("%+v", rows)
	}
}
