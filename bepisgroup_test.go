package main

import (
	"context"
	"strings"
	"testing"
)

func TestBepisAuthorGroups(t *testing.T) {
	recs := map[int]bepisRecord{
		1: {ID: 1, Authors: []string{"Old Name", "New Name"}},
		2: {ID: 2, Authors: []string{"New Name", "Old Name", "Anonymous"}},
		3: {ID: 3, Authors: []string{"Solo", "Anonymous"}},
		4: {ID: 4, Authors: []string{"Once A", "Once B"}}, // met on one card only
		5: {ID: 5, Authors: []string{"Solo"}},
		6: {ID: 6, Authors: []string{"New Name"}}, // New Name is also alone: not the same card set
	}
	if g := bepisAuthorGroups(recs); len(g) != 0 {
		t.Fatalf("%v", g)
	}
	recs[6] = bepisRecord{ID: 6, Authors: []string{"Other"}}
	g := bepisAuthorGroups(recs)
	if len(g) != 1 || strings.Join(g[0], "|") != "New Name|Old Name" {
		t.Fatalf("%v", g)
	}
}

func TestApplyBepisGroups(t *testing.T) {
	m := &Mapping{}
	m.init()
	m.Authors["author-new"] = &Author{Name: "New Name", Slug: "author-new", KoikatsuURL: authorURL("author-new")}
	n := m.applyBepis(map[string]int{"New Name": 3, "Old Name": 5, "Third": 4},
		nil, [][]string{{"New Name", "Old Name"}})
	if n != 1 {
		t.Fatalf("matched=%d", n)
	}
	b := m.Authors["author-new"].Bepis
	if len(b) != 2 || b[0].Name != "New Name" || b[0].Match != "name" || b[1].Name != "Old Name" || b[1].Match != "group" {
		t.Fatalf("%+v", b)
	}
	if g := m.BepisAuthors["Old Name"].Group; len(g) != 2 || g[0] != "Old Name" {
		t.Fatalf("group must be ordered by card count: %v", g)
	}
	if len(m.BepisAuthors["Third"].Group) != 0 || len(m.BepisAuthors["Third"].KoikatsuAuthorURLs) != 0 {
		t.Fatal("unexpected link")
	}
}

// Bepis names that always appear together become one row linking the name with the most cards.
func TestBepisOnlyGroupsMergeIntoOneRow(t *testing.T) {
	a := newTestApp(t)
	a.fileIdx = nil // newTestApp loads data/authors.json when present; its entries would add rows
	a.mapping.BepisAuthors["Alpha"] = &BepisAuthor{Name: "Alpha", URL: bepisAuthorURL("Alpha"), CardCount: 2, Group: []string{"Beta", "Alpha"}}
	a.mapping.BepisAuthors["Beta"] = &BepisAuthor{Name: "Beta", URL: bepisAuthorURL("Beta"), CardCount: 7, Group: []string{"Beta", "Alpha"}}
	a.mapping.BepisAuthors["Gamma"] = &BepisAuthor{Name: "Gamma", URL: bepisAuthorURL("Gamma"), CardCount: 1}
	rows := a.buildAuthorRows(context.Background(), nil, false)
	if len(rows) != 2 {
		t.Fatalf("%+v", rows)
	}
	var r *AuthorRow
	for i := range rows {
		if rows[i].Name == "Beta" {
			r = &rows[i]
		}
	}
	if r == nil || r.Links.Bepis != bepisAuthorURL("Beta") {
		t.Fatalf("%+v", rows)
	}
	if strings.Join(r.Alias, ",") != "Alpha" { // the row name (Beta, the name with the most cards) is not repeated as an alias
		t.Fatalf("alias %v", r.Alias)
	}
}
