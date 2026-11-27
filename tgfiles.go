package main

// File-host links found in Telegram messages (mega, gofile, catbox, mediafire, pixeldrain,
// dropbox, google drive). They are collected during the same chat scan as the pixiv links, into
// their own file (data/telegram_links.json) because there can be thousands of them and nothing
// else reads them. No network is needed, so this is unit-tested.

import (
	"regexp"
	"sort"
	"strings"
)

// tgFileHosts maps the host (and every subdomain of it) to the service name saved with a link.
var tgFileHosts = map[string]string{
	"mega.nz":                      "mega",
	"mega.co.nz":                   "mega",
	"mega.io":                      "mega",
	"gofile.io":                    "gofile",
	"catbox.moe":                   "catbox",
	"mediafire.com":                "mediafire",
	"pixeldrain.com":               "pixeldrain",
	"pixeldrain.net":               "pixeldrain",
	"dropbox.com":                  "dropbox",
	"dropboxusercontent.com":       "dropbox",
	"drive.google.com":             "gdrive",
	"drive.usercontent.google.com": "gdrive",
}

// A link is a known host plus a path (at least "/x"). The path runs to whitespace, a quote, an
// angle bracket or a CJK/full-width punctuation mark; trailing punctuation is trimmed after.
var tgFileLinkRe = func() *regexp.Regexp {
	var hosts []string
	for h := range tgFileHosts {
		hosts = append(hosts, regexp.QuoteMeta(h))
	}
	sort.Strings(hosts)
	return regexp.MustCompile(`(?i)(?:https?://)?((?:[a-z0-9-]+\.)*(?:` + strings.Join(hosts, "|") + `))` +
		`(/[^\s<>"'` + "`" + `\x{3000}-\x{303F}\x{FF00}-\x{FFEF}]+)`)
}()

// tgFileLink is one extracted link: the normalized URL (https, lower-case host) and its service.
type tgFileLink struct {
	URL     string
	Service string
}

// extractFileLinks returns the file-host links in the texts, each once, in order of appearance.
// Fragments are kept: a mega link is useless without its #key.
func extractFileLinks(texts ...string) []tgFileLink {
	var out []tgFileLink
	seen := map[string]bool{}
	for _, t := range texts {
		for _, idx := range tgFileLinkRe.FindAllStringSubmatchIndex(t, -1) {
			start := idx[0]
			hasScheme := strings.HasPrefix(strings.ToLower(t[start:]), "http")
			if !hasScheme && start > 0 {
				// "notmega.nz/x", "a@mega.nz/x", "evil.com/mega.nz/x" are not mega links
				if c := t[start-1]; c == '.' || c == '-' || c == '_' || c == '@' || c == '/' ||
					c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' {
					continue
				}
			}
			host := strings.ToLower(t[idx[2]:idx[3]])
			path := strings.TrimRight(t[idx[4]:idx[5]], ".,;:!?)]}>*")
			if len(path) < 2 {
				continue
			}
			u := "https://" + host + path
			if seen[u] {
				continue
			}
			seen[u] = true
			out = append(out, tgFileLink{URL: u, Service: tgFileService(host)})
		}
	}
	return out
}

func tgFileService(host string) string {
	for h, s := range tgFileHosts {
		if host == h || strings.HasSuffix(host, "."+h) {
			return s
		}
	}
	return ""
}

// tgFileEntry is a saved link and every chat it was seen in (see tgSighting).
type tgFileEntry struct {
	URL       string       `json:"url"`
	Service   string       `json:"service"`
	Sightings []tgSighting `json:"sightings"`
}

// tgLinkChat is the incremental state of one chat for the links file. It is kept apart from the
// pixiv file's chat state so that either file can be deleted and rebuilt on its own: a scan
// starts from the older of the two positions.
type tgLinkChat struct {
	LastMessageID int    `json:"last_message_id"`
	UpdatedAt     string `json:"updated_at,omitempty"`
}

// tgLinks is data/telegram_links.json.
type tgLinks struct {
	UpdatedAt string                  `json:"updated_at"`
	Chats     map[string]*tgLinkChat  `json:"chats"`
	Links     map[string]*tgFileEntry `json:"links"` // by URL
}

func (l *tgLinks) init() {
	if l.Chats == nil {
		l.Chats = map[string]*tgLinkChat{}
	}
	if l.Links == nil {
		l.Links = map[string]*tgFileEntry{}
	}
}

// add records the file-host links of one message.
func (l *tgLinks) add(chat string, m tgMessage) {
	for _, f := range extractFileLinks(m.Texts...) {
		e := l.Links[f.URL]
		if e == nil {
			e = &tgFileEntry{URL: f.URL, Service: f.Service}
			l.Links[f.URL] = e
		}
		seeSighting(&e.Sightings, chat, m)
	}
}
