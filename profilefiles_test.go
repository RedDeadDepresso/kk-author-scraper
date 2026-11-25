package main

import "testing"

func TestProfileFileLinks(t *testing.T) {
	m := &Mapping{PixivProfiles: map[string]*PixivProfile{
		"1": {Links: []ProfileLink{
			{URL: "https://www.patreon.com/x"},
			{URL: "https://mega.nz/folder/aaa#bbb"},
			{URL: "https://drive.google.com/drive/folders/first"},
		}},
		"2": {Links: []ProfileLink{
			{URL: "https://mega.nz/folder/second"},
			{URL: "https://drive.google.com/drive/folders/second"},
		}},
	}}
	mega, drive := m.profileFileLinks([]string{"1", "2", "3"})
	if mega != "https://mega.nz/folder/aaa#bbb" || drive != "https://drive.google.com/drive/folders/first" {
		t.Fatalf("%q %q", mega, drive)
	}
	if mega, drive := m.profileFileLinks([]string{"3"}); mega != "" || drive != "" {
		t.Fatalf("%q %q", mega, drive)
	}
}

func TestSetScene(t *testing.T) {
	var d DownloadLinks
	d.setScene("https://buzzheavier.com/abc")
	d.setScene("https://fileditch.com/x")
	if d.Buzzheavier != "https://buzzheavier.com/abc" || d.Fileditch != "https://fileditch.com/x" {
		t.Fatalf("%+v", d)
	}
}
