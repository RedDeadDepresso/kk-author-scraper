package main

import (
	"os"
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
	const path = "/mnt/user-data/uploads/authors.json"
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
