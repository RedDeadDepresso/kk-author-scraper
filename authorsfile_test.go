package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPixivRefs(t *testing.T) {
	u, a := pixivRefs([]string{
		"https://www.pixiv.net/users/6643505/artworks",
		"https://www.pixiv.net/en/users/12",
		"https://www.pixiv.net/member.php?id=34",
		"https://www.pixiv.net/artworks/148202067",
		"https://x.com/foo",
	})
	if len(u) != 3 || len(a) != 1 || a[0] != "148202067" {
		t.Fatalf("%v %v", u, a)
	}
}

func TestRealAuthorsFile(t *testing.T) {
	const path = "data/authors.json"
	if _, err := os.Stat(path); err != nil {
		t.Skip("no sample")
	}
	ix, err := loadAuthorsFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// authors seen on the sample koikatsucards page
	for _, n := range []string{"Decopong", "SolityL", "〇銭好", "朝暮", "審判", "Istia", "ひがし", "をりてぃす", "asgf", "Alucid", "sam"} {
		e, amb := ix.find(n)
		if e == nil {
			t.Logf("%-10s -> none (ambiguous=%v)", n, amb)
			continue
		}
		u, art := pixivRefs(e.Links)
		t.Logf("%-10s -> %s users=%v artworks=%v", n, e.Name, u, art)
	}
	if e, _ := ix.find("alucid"); e == nil {
		t.Fatal("Alucid not found")
	} else if u, _ := pixivRefs(e.Links); len(u) != 1 || u[0] != "3986523" {
		t.Fatalf("%v", u)
	}
	// ambiguity stats
	amb, none := 0, 0
	for k, v := range ix.byName {
		if len(v) > 1 {
			amb++
			t.Logf("duplicate name key %q: %d entries", k, len(v))
		}
	}
	_ = none
	t.Logf("%d entries, %d duplicate primary names", ix.count, amb)
}

func TestCreatorHandles(t *testing.T) {
	got := creatorHandles([]string{
		"https://oct-nolazy.fanbox.cc/",
		"https://www.fanbox.cc/@Someone",
		"https://www.fanbox.cc/creator/1234",
		"https://www.patreon.com/a_fan",
		"https://www.patreon.com/cw/Abceefg",
		"https://www.patreon.com/c/arisaka190",
		"https://www.patreon.com/u32705220",
		"https://www.patreon.com/user?u=30902669",
		"https://www.patreon.com/Ravenant1/about",
		"https://patreon.com/izu914",
		"https://x.com/foo",
		"https://www.pixiv.net/users/1",
		"https://www.patreon.com/posts/12345",
	})
	want := []Handle{
		{"fanbox", false, "oct-nolazy"},
		{"fanbox", false, "someone"},
		{"fanbox", true, "1234"},
		{"patreon", false, "a_fan"},
		{"patreon", false, "abceefg"},
		{"patreon", false, "arisaka190"},
		{"patreon", true, "32705220"},
		{"patreon", true, "30902669"},
		{"patreon", false, "ravenant1"},
		{"patreon", false, "izu914"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%d: got %v want %v", i, got[i], want[i])
		}
	}
}

func TestAuthorsFileRoundTrip(t *testing.T) {
	rows := []AuthorRow{
		{Name: "<!>", Alias: []string{"a&b"}, Series: []string{}, Links: AuthorLinks{
			Pixiv: "https://www.pixiv.net/users/12", Pawchive: "https://pawchive.pw/patreon/user/600"}},
		{Name: "Fan", Alias: []string{}, Series: []string{}, Links: AuthorLinks{
			Pawchive: "https://pawchive.pw/fanbox/user/34"}},
	}
	path := filepath.Join(t.TempDir(), "authors.json")
	if err := saveJSONIndent(path, rows); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), "\n  {\n    \"name\": ") || !strings.Contains(string(b), `"<!>"`) || !strings.Contains(string(b), `"a&b"`) {
		t.Fatalf("expected indented, unescaped output:\n%s", b)
	}
	ix, err := loadAuthorsFile(path)
	if err != nil || ix.count != 2 {
		t.Fatalf("%v %+v", err, ix)
	}
	e := ix.entries[0] // a name without letters or digits cannot be looked up by name
	// the stored pawchive creator comes back as a creator link, so pawchive matching keeps working
	if e == nil || len(e.Links) != 2 {
		t.Fatalf("%+v", e)
	}
	if u, _ := pixivRefs(e.Links); len(u) != 1 || u[0] != "12" {
		t.Fatalf("%v", e.Links)
	}
	if h := creatorHandles(e.Links); len(h) != 1 || h[0] != (Handle{"patreon", true, "600"}) {
		t.Fatalf("%v", h)
	}
	if f, _ := ix.find("Fan"); f == nil || creatorHandles(f.Links)[0] != (Handle{"fanbox", true, "34"}) {
		t.Fatalf("%+v", f)
	}
}

// The committed data/authors.json must be exactly what the scraper itself would write,
// so a run without changes leaves no diff.
func TestDataAuthorsFileIsCanonical(t *testing.T) {
	const path = "data/authors.json"
	orig, err := os.ReadFile(path)
	if err != nil {
		t.Skip("no data/authors.json")
	}
	var rows []AuthorRow
	if err := json.Unmarshal(orig, &rows); err != nil {
		t.Fatal(err)
	}
	tmp := filepath.Join(t.TempDir(), "authors.json")
	if err := saveJSONIndent(tmp, rows); err != nil {
		t.Fatal(err)
	}
	if again, _ := os.ReadFile(tmp); !bytes.Equal(orig, again) {
		t.Fatal("data/authors.json differs from the scraper's own output format")
	}
}
