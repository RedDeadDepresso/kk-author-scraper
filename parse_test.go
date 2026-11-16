package main

import (
	"os"
	"testing"
)

func TestParseCards(t *testing.T) {
	b, err := os.ReadFile("/mnt/user-data/uploads/koikatsucards.html")
	if err != nil {
		t.Skip("sample html not available")
	}
	res := parsePage(string(b))
	cards := res.Cards
	if res.Links != 12 || !res.NextKnown || !res.HasNext {
		t.Fatalf("links=%d next=%v/%v", res.Links, res.NextKnown, res.HasNext)
	}
	if len(cards) != 12 {
		t.Fatalf("got %d cards", len(cards))
	}
	if cards[0].Work != "Blue Archive" || cards[1].Work != "Azur Lane" || cards[11].Work != "The Idolmaster" {
		t.Fatalf("works: %q %q %q", cards[0].Work, cards[1].Work, cards[11].Work)
	}
	for _, c := range cards {
		t.Logf("%+v", c)
		if c.Author == "" {
			t.Errorf("missing author for %s", c.PixivID)
		}
	}
}

func TestParsePageNonPixivAndLastPage(t *testing.T) {
	html := `<a href="/contents/pixiv-111"><h3>A</h3><p class="muted">Au</p></a>
<a href="/contents/twitter-222"><h3>B</h3><p class="muted">Bu</p></a>
<a href="/contents/pixiv-333_2"><h3>C</h3></a>
<a class="x" aria-disabled="true" href="/search?page=9">Next</a>`
	res := parsePage(html)
	if res.Links != 3 || len(res.Cards) != 1 || res.Cards[0].PixivID != "111" || len(res.Other) != 2 {
		t.Fatalf("%+v", res)
	}
	if !res.NextKnown || res.HasNext {
		t.Fatalf("next: %+v", res)
	}
	if res := parsePage("<html>nothing</html>"); res.Links != 0 {
		t.Fatal("expected empty")
	}
}
