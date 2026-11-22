package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseHashtags(t *testing.T) {
	got := parseHashtags("# note\n#白龙 #As@hi, #Different_World\nA\n#BBQ")
	want := []string{"白龙", "As@hi", "Different_World", "BBQ"}
	if len(got) != len(want) {
		t.Fatalf("%v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%v", got)
		}
	}
}

func TestTelegramApply(t *testing.T) {
	dir := t.TempDir()
	tags := filepath.Join(dir, "tags.txt")
	ov := filepath.Join(dir, "ov.json")
	os.WriteFile(tags, []byte("#Haku #As@hi #shared #kard #wrong"), 0o644)
	os.WriteFile(ov, []byte(`{"Kardia": "#kard", "Wrongly": "", "Both": ["#Haku", "kard"]}`), 0o644)
	tt, err := loadTelegramTags(tags, ov)
	if err != nil {
		t.Fatal(err)
	}
	rows := []AuthorRow{
		{Name: "haku"}, // name match, case-insensitive
		{Name: "Ashi"}, // punctuation in the hashtag is ignored
		{Name: "Someone", Alias: []string{"Haku"}}, // alias of an already name-matched tag: skipped
		{Name: "A", Alias: []string{"shared"}},     // two rows share this alias: ambiguous
		{Name: "B", Alias: []string{"shared"}},
		{Name: "Kardia"},                            // override
		{Name: "Wrongly", Alias: []string{"wrong"}}, // override "" suppresses the link
		{Name: "Solo", Alias: []string{"nothing"}},
		{Name: "Both"},                                        // a list override gives several hashtags
		{Name: "Multi", Alias: []string{"Ashi2", "wrongish"}}, // unused aliases: no link
	}
	if n := tt.apply(rows); n != 4 {
		t.Fatalf("linked %d: %+v", n, rows)
	}
	eq := func(i int, want ...string) {
		t.Helper()
		if len(rows[i].Telegram) != len(want) {
			t.Fatalf("row %d: %q, want %q", i, rows[i].Telegram, want)
		}
		for j := range want {
			if rows[i].Telegram[j] != want[j] {
				t.Fatalf("row %d: %q, want %q", i, rows[i].Telegram, want)
			}
		}
	}
	eq(0, "#Haku")
	eq(1, "#As@hi")
	eq(5, "#kard")
	eq(8, "#Haku", "#kard")
	for _, i := range []int{2, 3, 4, 6, 7, 9} {
		eq(i)
	}
}

func TestTelegramMissingFiles(t *testing.T) {
	tt, err := loadTelegramTags("/nonexistent/a.txt", "/nonexistent/b.json")
	if err != nil || len(tt.byNorm) != 0 || len(tt.overrides) != 0 {
		t.Fatalf("%v %+v", err, tt)
	}
}

func TestTelegramAliasesAddExtraTags(t *testing.T) {
	dir := t.TempDir()
	tags := filepath.Join(dir, "tags.txt")
	os.WriteFile(tags, []byte("#hatoko #Horai #Third"), 0o644)
	tt, err := loadTelegramTags(tags, filepath.Join(dir, "none.json"))
	if err != nil {
		t.Fatal(err)
	}
	rows := []AuthorRow{{Name: "HATOKO", Alias: []string{"Third", "Horai"}}}
	tt.apply(rows)
	if got := []string(rows[0].Telegram); len(got) != 3 || got[0] != "#hatoko" || got[1] != "#Horai" || got[2] != "#Third" {
		t.Fatalf("%q", got) // name tag first, then alias tags in tags-file order
	}
}

func TestTagListJSON(t *testing.T) {
	var r AuthorRow
	for in, want := range map[string]int{`{"telegram":"#a"}`: 1, `{"telegram":""}`: 0, `{"telegram":null}`: 0, `{"telegram":["#a","#b"]}`: 2} {
		r = AuthorRow{}
		if err := json.Unmarshal([]byte(in), &r); err != nil || len(r.Telegram) != want {
			t.Fatalf("%s: %v %q", in, err, r.Telegram)
		}
	}
	b, _ := json.Marshal(AuthorRow{})
	if !strings.Contains(string(b), `"telegram":[]`) {
		t.Fatalf("%s", b)
	}
}
