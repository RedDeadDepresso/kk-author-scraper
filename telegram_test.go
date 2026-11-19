package main

import (
	"os"
	"path/filepath"
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
	os.WriteFile(ov, []byte(`{"Kardia": "#kard", "Wrongly": ""}`), 0o644)
	tt, err := loadTelegramTags(tags, ov)
	if err != nil {
		t.Fatal(err)
	}
	rows := []AuthorRow{
		{Name: "haku"},                          // name match, case-insensitive
		{Name: "Ashi"},                          // punctuation in the hashtag is ignored
		{Name: "Someone", Alias: []string{"Haku"}}, // alias of an already name-matched tag: skipped
		{Name: "A", Alias: []string{"shared"}},  // two rows share this alias: ambiguous
		{Name: "B", Alias: []string{"shared"}},
		{Name: "Kardia"},  // override
		{Name: "Wrongly", Alias: []string{"wrong"}}, // override "" suppresses the link
		{Name: "Solo", Alias: []string{"nothing"}},
	}
	if n := tt.apply(rows); n != 3 {
		t.Fatalf("linked %d: %+v", n, rows)
	}
	if rows[0].Links.Telegram != "tg://search_hashtag?hashtag=Haku" {
		t.Fatalf("%q", rows[0].Links.Telegram)
	}
	if rows[1].Links.Telegram != "tg://search_hashtag?hashtag=As%40hi" {
		t.Fatalf("%q", rows[1].Links.Telegram)
	}
	if rows[5].Links.Telegram != "tg://search_hashtag?hashtag=kard" {
		t.Fatalf("%q", rows[5].Links.Telegram)
	}
	for _, i := range []int{2, 3, 4, 6, 7} {
		if rows[i].Links.Telegram != "" {
			t.Fatalf("row %d: %q", i, rows[i].Links.Telegram)
		}
	}
}

func TestTelegramMissingFiles(t *testing.T) {
	tt, err := loadTelegramTags("/nonexistent/a.txt", "/nonexistent/b.json")
	if err != nil || len(tt.byNorm) != 0 || len(tt.overrides) != 0 {
		t.Fatalf("%v %+v", err, tt)
	}
}
