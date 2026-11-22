package main

import (
	"context"
	"testing"
)

// Names made only of symbols all normalize to "", so they must not share one memo entry.
func TestFileAccountsSymbolNamesAreIndependent(t *testing.T) {
	a := newTestApp(t)
	a.fileIdx = &authorIndex{byName: map[string][]*fileAuthor{}, byAlias: map[string][]*fileAuthor{}, byExact: map[string][]*fileAuthor{}}
	for _, e := range []*fileAuthor{
		{Name: ".", Links: []string{"https://www.pixiv.net/users/1"}},
		{Name: ":)", Links: []string{"https://www.pixiv.net/users/2"}},
	} {
		a.fileIdx.byExact[e.Name] = []*fileAuthor{e}
	}
	ctx := context.Background()
	r1, _ := a.fileAccounts(ctx, ".")
	r2, _ := a.fileAccounts(ctx, ":)")
	if len(r1.IDs) != 1 || r1.IDs[0] != "1" || len(r2.IDs) != 1 || r2.IDs[0] != "2" {
		t.Fatalf("%v %v", r1.IDs, r2.IDs)
	}
}

// An entry whose name has letters can still be found by a symbol-only alias.
func TestSymbolAliasFindsEntry(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/authors.json"
	if err := saveJSONIndent(path, []AuthorRow{{Name: "Real", Alias: []string{"●•●", "wssb"}}}); err != nil {
		t.Fatal(err)
	}
	ix, err := loadAuthorsFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if e, _ := ix.find("●•●"); e == nil || e.Name != "Real" {
		t.Fatalf("%+v", e)
	}
}
