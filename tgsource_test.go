package main

import (
	"iter"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestParseTelegramChat(t *testing.T) {
	for _, tc := range []struct{ in, ident, key string }{
		{"https://t.me/SomeChannel", "SomeChannel", "somechannel"},
		{"t.me/SomeChannel/123", "SomeChannel", "somechannel"},
		{"https://t.me/s/SomeChannel", "SomeChannel", "somechannel"},
		{"@SomeChannel", "SomeChannel", "somechannel"},
		{"https://t.me/c/1234567890", "1234567890", "c/1234567890"},
		{"https://t.me/c/1234567890/42/77", "1234567890", "c/1234567890"},
		{"-1001234567890", "1234567890", "c/1234567890"},
		{"1234567890", "1234567890", "c/1234567890"},
	} {
		ident, key, err := parseTelegramChat(tc.in)
		if err != nil || ident != tc.ident || key != tc.key {
			t.Errorf("%q: got %q %q %v, want %q %q", tc.in, ident, key, err, tc.ident, tc.key)
		}
	}
	for _, bad := range []string{"", "https://example.com/x", "https://t.me/+AbCdEf", "https://t.me/joinchat/AbC", "https://t.me/", "https://t.me/c/x", "@"} {
		if _, _, err := parseTelegramChat(bad); err == nil {
			t.Errorf("%q: expected an error", bad)
		}
	}
}

func TestLoadTelegramChats(t *testing.T) {
	dir := t.TempDir()
	write := func(s string) string {
		p := filepath.Join(dir, "c.json")
		os.WriteFile(p, []byte(s), 0o644)
		return p
	}
	got, err := loadTelegramChats(write(`{"chats": ["https://t.me/a", {"link": "t.me/b", "post_topic": 5}, {"link": "t.me/c", "topic": 6, "no_lookup": true}]}`))
	want := []tgChatCfg{{Link: "https://t.me/a"}, {Link: "t.me/b", PostTopic: 5}, {Link: "t.me/c", PostTopic: 6, NoLookup: true}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("%v %v", got, err)
	}
	if got, err = loadTelegramChats(write(`["t.me/a"]`)); err != nil || len(got) != 1 {
		t.Fatalf("bare array: %v %v", got, err)
	}
	for _, bad := range []string{`{"chats": ["t.me/a", "https://t.me/A/5"]}`, `{"chats": ["nope"]}`, `{"chats": [{"link": "t.me/a", "post_topic": -1}]}`, `{`} {
		if _, err := loadTelegramChats(write(bad)); err == nil {
			t.Errorf("%s: expected an error", bad)
		}
	}
}

func TestExtractPixiv(t *testing.T) {
	text := `https://www.pixiv.net/users/123 and pixiv.net/en/users/123/artworks
https://www.pixiv.net/artworks/9001 http://www.pixiv.net/ja/artworks/9002?x=1
https://www.pixiv.net/member.php?id=77 https://www.pixiv.net/member_illust.php?mode=medium&illust_id=9003
https://pixiv.net/i/9004 https://fanbox.cc/@x https://www.pixiv.net/fanbox/creator/55 https://example.com/users/5`
	users, posts := extractPixiv(text, "https://www.pixiv.net/users/88")
	if want := []string{"123", "77", "88"}; !reflect.DeepEqual(users, want) {
		t.Errorf("users %v", users)
	}
	if want := []string{"9001", "9002", "9003", "9004"}; !reflect.DeepEqual(posts, want) {
		t.Errorf("posts %v", posts)
	}
}

func seq(msgs ...tgMessage) iter.Seq2[tgMessage, error] {
	return func(yield func(tgMessage, error) bool) {
		for _, m := range msgs { // newest first, as Telegram returns them
			if !yield(m, nil) {
				return
			}
		}
	}
}

func TestScanIncrementalAndTopic(t *testing.T) {
	e := &tgExport{}
	e.init()
	cfg := tgChatCfg{Link: "t.me/x", PostTopic: 5}
	m := func(id, topic int, text string) tgMessage { return tgMessage{ID: id, Topic: topic, Date: 1700000000, Texts: []string{text}} }

	n, nu, np, err := e.scanChat("x", cfg, seq(
		m(30, 5, "pixiv.net/artworks/3 pixiv.net/users/1"),
		m(20, 1, "pixiv.net/artworks/2 pixiv.net/users/2"), // other topic: user yes, post no
		m(10, 5, "pixiv.net/artworks/1"),
	))
	if err != nil || n != 3 || nu != 2 || np != 2 {
		t.Fatalf("first run: %d %d %d %v", n, nu, np, err)
	}
	if _, ok := e.PixivPosts["2"]; ok {
		t.Error("post of another topic was kept")
	}
	if e.Chats["x"].LastMessageID != 30 {
		t.Fatalf("last id %d", e.Chats["x"].LastMessageID)
	}

	// second run: stops at message 30, never reads 20 again
	read := 0
	counting := func(yield func(tgMessage, error) bool) {
		for _, msg := range []tgMessage{m(41, 5, "pixiv.net/artworks/4"), m(30, 5, "pixiv.net/artworks/3"), m(20, 5, "pixiv.net/artworks/2")} {
			read++
			if !yield(msg, nil) {
				return
			}
		}
	}
	n, _, np, err = e.scanChat("x", cfg, counting)
	if err != nil || n != 1 || np != 1 || read != 2 || e.Chats["x"].LastMessageID != 41 {
		t.Fatalf("second run: n=%d new posts=%d read=%d last=%d err=%v", n, np, read, e.Chats["x"].LastMessageID, err)
	}

	// changing the topic starts over
	cfg.PostTopic = 0
	n, _, np, _ = e.scanChat("x", cfg, seq(m(41, 5, "pixiv.net/artworks/4"), m(20, 1, "pixiv.net/artworks/2")))
	if n != 2 || np != 1 {
		t.Fatalf("topic change: %d %d", n, np)
	}
}

func TestScanErrorKeepsLastID(t *testing.T) {
	e := &tgExport{}
	e.init()
	e.Chats["x"] = &tgChatState{LastMessageID: 10}
	failing := func(yield func(tgMessage, error) bool) {
		yield(tgMessage{ID: 50, Texts: []string{"pixiv.net/users/9"}}, nil)
		yield(tgMessage{}, os.ErrDeadlineExceeded)
	}
	if _, _, _, err := e.scanChat("x", tgChatCfg{Link: "t.me/x"}, failing); err == nil {
		t.Fatal("expected error")
	}
	if e.Chats["x"].LastMessageID != 10 {
		t.Errorf("last id moved to %d after a failed export", e.Chats["x"].LastMessageID)
	}
	if e.PixivUsers["9"] == nil {
		t.Error("link found before the failure was dropped")
	}
}

func TestRecordKeepsOldestSighting(t *testing.T) {
	e := &tgExport{}
	e.init()
	e.record("x", tgMessage{ID: 30, Texts: []string{"pixiv.net/users/1"}}, 0)
	e.record("x", tgMessage{ID: 10, Texts: []string{"pixiv.net/users/1"}}, 0)
	e.record("x", tgMessage{ID: 20, Texts: []string{"pixiv.net/users/1"}}, 0)
	got := e.PixivUsers["1"]
	if len(got.Sightings) != 1 || got.Sightings[0].MessageID != 10 || got.URL != "https://www.pixiv.net/users/1" {
		t.Errorf("%+v", got)
	}
	// another chat adds its own sighting instead of replacing or being ignored
	e.record("a", tgMessage{ID: 99, Texts: []string{"pixiv.net/users/1"}}, 0)
	if got := e.PixivUsers["1"]; len(got.Sightings) != 2 || got.Sightings[0].Chat != "a" || got.Sightings[1].Chat != "x" || got.Sightings[1].MessageID != 10 {
		t.Errorf("%+v", got.Sightings)
	}
}

func TestTelegramAccountsBecomeAuthors(t *testing.T) {
	a := kknowTestApp(t)
	// the accounts of the kknow test app come from Telegram instead; 125 and 126 are known already
	a.kknow = &KknowCache{}
	a.tgPixiv = &tgExport{}
	a.tgPixiv.init()
	for _, id := range []string{"123", "124", "125", "126"} {
		a.tgPixiv.PixivUsers[id] = &tgPixivUser{ID: id}
	}
	if got := a.extraAccounts(); !reflect.DeepEqual(got, []string{"123", "124", "125", "126"}) {
		t.Fatalf("extraAccounts %v", got)
	}
	if n := a.addKknowAuthors(); n != 1 {
		t.Fatalf("added %d, want 1 (only 123 is new and has a profile)", n)
	}
	if e, _ := a.fileIdx.find("Foo"); e == nil || len(e.Links) != 1 || e.Links[0] != pixivBase+"/users/123" {
		t.Errorf("no authors entry for the telegram account: %+v", e)
	}
}

func TestExtractFileLinks(t *testing.T) {
	text := `https://mega.nz/file/AbCd1234#KeY_-9 and http://MEGA.nz/folder/xyz#k.
(https://gofile.io/d/abc123) www.mediafire.com/file/q1/name.zip/file, catbox: files.catbox.moe/x9y8.zip
<https://pixeldrain.com/u/Pd12>」https://www.dropbox.com/scl/fi/aa/b.zip?rlkey=k&dl=0
https://drive.google.com/file/d/1AbC/view?usp=sharing https://docs.google.com/document/d/1/edit
notmega.nz/file/no a@mega.nz/x https://evil.com/mega.nz/file/no https://mega.nz/ https://mega.nz/file/AbCd1234#KeY_-9`
	var got []string
	for _, l := range extractFileLinks(text) {
		got = append(got, l.Service+" "+l.URL)
	}
	want := []string{
		"mega https://mega.nz/file/AbCd1234#KeY_-9",
		"mega https://mega.nz/folder/xyz#k",
		"gofile https://gofile.io/d/abc123",
		"mediafire https://www.mediafire.com/file/q1/name.zip/file",
		"catbox https://files.catbox.moe/x9y8.zip",
		"pixeldrain https://pixeldrain.com/u/Pd12",
		"dropbox https://www.dropbox.com/scl/fi/aa/b.zip?rlkey=k&dl=0",
		"gdrive https://drive.google.com/file/d/1AbC/view?usp=sharing",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

func TestScanCollectsFileLinksAndResumesPerFile(t *testing.T) {
	e := &tgExport{links: &tgLinks{}}
	e.init()
	e.links.init()
	cfg := tgChatCfg{Link: "t.me/x", PostTopic: 5}
	m := func(id, topic int, text string) tgMessage { return tgMessage{ID: id, Topic: topic, Date: 1700000000, Texts: []string{text}} }
	// links are collected from every topic, even when post_topic limits the pixiv posts
	if _, _, _, err := e.scanChat("x", cfg, seq(m(30, 1, "https://gofile.io/d/b"), m(10, 5, "https://gofile.io/d/a pixiv.net/artworks/1"))); err != nil {
		t.Fatal(err)
	}
	e.record("y", m(3, 1, "https://gofile.io/d/b"), 0)
	if len(e.links.Links) != 2 || len(e.links.Links["https://gofile.io/d/b"].Sightings) != 2 || e.links.Chats["x"].LastMessageID != 30 {
		t.Fatalf("%+v", e.links)
	}
	// a deleted links file is filled again; the pixiv state alone would have stopped at 30
	e.links = &tgLinks{}
	e.links.init()
	read := 0
	counting := func(yield func(tgMessage, error) bool) {
		for _, msg := range []tgMessage{m(30, 1, "https://gofile.io/d/b"), m(10, 5, "https://gofile.io/d/a")} {
			read++
			if !yield(msg, nil) {
				return
			}
		}
	}
	if _, _, _, err := e.scanChat("x", cfg, counting); err != nil || read != 2 || len(e.links.Links) != 2 || e.Chats["x"].LastMessageID != 30 {
		t.Fatalf("read=%d links=%d last=%d err=%v", read, len(e.links.Links), e.Chats["x"].LastMessageID, err)
	}
}
