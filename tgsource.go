package main

// Telegram source: chat links listed in a JSON file are exported (using gotd/td)
// and every pixiv user link and pixiv post link found in the messages is collected into
// data/telegram_pixiv.json (file-host links into data/telegram_links.json, see tgfiles.go). This file holds everything that needs no network, so it is
// unit-tested; tgclient.go does the Telegram part.

import (
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Chat list (config/telegram_chats.json)
// ---------------------------------------------------------------------------

// tgChatCfg is one entry of the chat list. In JSON it is either a bare link string or
//
//	{"link": "https://t.me/somegroup", "post_topic": 42}
//
// Pixiv user links are collected from every message of the chat. Pixiv post links are
// collected from every message too, unless post_topic is set: then only from the messages
// of that forum topic (the topic id is the number in a topic link, https://t.me/c/<chat>/<topic>).
//
// no_lookup keeps the chat's messages out of the lookup file for the browser extension, which is
// public (use it for private chats: their message links only open for members).
type tgChatCfg struct {
	Link      string `json:"link"`
	PostTopic int    `json:"post_topic,omitempty"`
	NoLookup  bool   `json:"no_lookup,omitempty"`

	order int // 1-based position in the chat list (set by loadTelegramChats)
}

func (c *tgChatCfg) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*c = tgChatCfg{Link: s}
		return nil
	}
	var o struct {
		Link       string `json:"link"`
		PostTopic  int    `json:"post_topic"`
		Topic      int    `json:"topic"` // short alias of post_topic
		NoLookup   bool   `json:"no_lookup"`
	}
	if err := json.Unmarshal(b, &o); err != nil {
		return err
	}
	c.Link = o.Link
	c.NoLookup = o.NoLookup
	c.PostTopic = o.PostTopic
	if c.PostTopic == 0 {
		c.PostTopic = o.Topic
	}
	return nil
}

// loadTelegramChats reads the chat list: either {"chats": [...]} or a bare array.
func loadTelegramChats(path string) ([]tgChatCfg, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	b = []byte(strings.TrimSpace(string(b)))
	var list []tgChatCfg
	if len(b) > 0 && b[0] == '[' {
		err = json.Unmarshal(b, &list)
	} else {
		var o struct {
			Chats []tgChatCfg `json:"chats"`
		}
		err = json.Unmarshal(b, &o)
		list = o.Chats
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	seen := map[string]bool{}
	var out []tgChatCfg
	for _, c := range list {
		_, key, err := parseTelegramChat(c.Link)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if seen[key] {
			return nil, fmt.Errorf("%s: chat %q is listed twice", path, c.Link)
		}
		seen[key] = true
		if c.PostTopic < 0 {
			return nil, fmt.Errorf("%s: chat %q: post_topic must be a topic id", path, c.Link)
		}
		c.order = len(out) + 1
		out = append(out, c)
	}
	return out, nil
}

// parseTelegramChat turns a chat link into what tdl resolves a chat from (ident: a username or
// a numeric channel id) and a stable key for the saved state.
//
// Accepted: https://t.me/name, t.me/name/123 (the message part is ignored), @name,
// https://t.me/c/1234567890 (private chat/channel, the id as in message links),
// -1001234567890 or 1234567890. Invite links (t.me/+xxxx) cannot be resolved without joining;
// use the numeric id of the chat instead.
func parseTelegramChat(link string) (ident, key string, err error) {
	s := strings.TrimSpace(link)
	if s == "" {
		return "", "", errors.New("empty chat link")
	}
	if strings.HasPrefix(s, "@") {
		name := strings.TrimPrefix(s, "@")
		if name == "" {
			return "", "", fmt.Errorf("invalid chat %q", link)
		}
		return name, strings.ToLower(name), nil
	}
	if n, e := strconv.ParseInt(s, 10, 64); e == nil {
		return tgNumericChat(n)
	}
	raw := s
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, e := url.Parse(raw)
	if e != nil {
		return "", "", fmt.Errorf("invalid chat link %q: %w", link, e)
	}
	switch strings.ToLower(strings.TrimPrefix(u.Host, "www.")) {
	case "t.me", "telegram.me", "telegram.dog":
	default:
		return "", "", fmt.Errorf("invalid chat link %q: not a t.me link", link)
	}
	var parts []string
	for _, p := range strings.Split(u.Path, "/") {
		if p != "" {
			parts = append(parts, p)
		}
	}
	if len(parts) == 0 {
		return "", "", fmt.Errorf("invalid chat link %q: no chat in it", link)
	}
	if parts[0] == "s" && len(parts) > 1 { // t.me/s/name is the web preview of t.me/name
		parts = parts[1:]
	}
	switch {
	case parts[0] == "c":
		if len(parts) < 2 {
			return "", "", fmt.Errorf("invalid chat link %q: no chat id after /c/", link)
		}
		n, e := strconv.ParseInt(parts[1], 10, 64)
		if e != nil || n <= 0 {
			return "", "", fmt.Errorf("invalid chat link %q: %q is not a chat id", link, parts[1])
		}
		return strconv.FormatInt(n, 10), "c/" + strconv.FormatInt(n, 10), nil
	case parts[0] == "joinchat" || strings.HasPrefix(parts[0], "+"):
		return "", "", fmt.Errorf("invite link %q cannot be used: put the chat's t.me/c/<id> link or numeric id in the list instead", link)
	}
	return parts[0], strings.ToLower(parts[0]), nil
}

func tgNumericChat(n int64) (string, string, error) {
	s := strconv.FormatInt(n, 10)
	s = strings.TrimPrefix(strings.TrimPrefix(s, "-100"), "-")
	if s == "" || s == "0" {
		return "", "", fmt.Errorf("invalid chat id %d", n)
	}
	return s, "c/" + s, nil
}

// ---------------------------------------------------------------------------
// Pixiv link extraction
// ---------------------------------------------------------------------------

const pixivLang = `(?:[a-z]{2}(?:_[a-z]{2})?/)?` // en/, ja/, zh_tw/ ...

var (
	pixivUserRes = []*regexp.Regexp{
		regexp.MustCompile(`(?i)\bpixiv\.net/` + pixivLang + `users/(\d+)`),
		regexp.MustCompile(`(?i)\bpixiv\.net/member\.php\?(?:[^\s#]*&)?id=(\d+)`),
	}
	pixivPostRes = []*regexp.Regexp{
		regexp.MustCompile(`(?i)\bpixiv\.net/` + pixivLang + `artworks/(\d+)`),
		regexp.MustCompile(`(?i)\bpixiv\.net/member_illust\.php\?(?:[^\s#]*&)?illust_id=(\d+)`),
		regexp.MustCompile(`(?i)\bpixiv\.net/i/(\d+)`),
	}
)

func pixivUserLink(id string) string { return "https://www.pixiv.net/users/" + id }
func pixivPostLink(id string) string { return "https://www.pixiv.net/artworks/" + id }

// extractPixiv returns the pixiv user ids and post (artwork) ids mentioned in the texts, each
// once, in order of appearance. Old member.php / member_illust.php links are understood too.
func extractPixiv(texts ...string) (users, posts []string) {
	seenU, seenP := map[string]bool{}, map[string]bool{}
	for _, t := range texts {
		for _, re := range pixivUserRes {
			for _, m := range re.FindAllStringSubmatch(t, -1) {
				if !seenU[m[1]] {
					seenU[m[1]] = true
					users = append(users, m[1])
				}
			}
		}
		for _, re := range pixivPostRes {
			for _, m := range re.FindAllStringSubmatch(t, -1) {
				if !seenP[m[1]] {
					seenP[m[1]] = true
					posts = append(posts, m[1])
				}
			}
		}
	}
	return users, posts
}

// ---------------------------------------------------------------------------
// Saved result (data/telegram_pixiv.json)
// ---------------------------------------------------------------------------

// tgChatState is what is remembered of one chat between runs.
type tgChatState struct {
	Link          string `json:"link"`
	Title         string `json:"title,omitempty"`
	PostTopic     int    `json:"post_topic,omitempty"`
	NoLookup      bool   `json:"no_lookup,omitempty"` // kept out of the lookup file
	Order         int    `json:"order,omitempty"`     // 1-based position in the chat list: the lookup file prefers the lower one for posts
	LastMessageID int    `json:"last_message_id"` // newest message of the last complete export
	Scanned       int    `json:"messages_scanned"`
	UpdatedAt     string `json:"updated_at,omitempty"`
}

// tgSighting is where a link was seen in one chat: the oldest message of that chat that has it.
type tgSighting struct {
	Chat      string `json:"chat"`
	MessageID int    `json:"message_id"`
	Topic     int    `json:"topic,omitempty"`
	Date      string `json:"date,omitempty"`
}

// tgPixivLink is a pixiv account or post and every chat it was seen in (one sighting per chat,
// sorted by chat key). The lookup file picks among them (see tgExport.lookup), so a post that a
// private chat saw first can still point at a public chat.
type tgPixivLink struct {
	ID        string       `json:"id"`
	URL       string       `json:"url"`
	Sightings []tgSighting `json:"sightings"`
}

type (
	tgPixivUser = tgPixivLink
	tgPixivPost = tgPixivLink
)

// see records a sighting in chat (see seeSighting) and reports whether the link was unknown.
func (l *tgPixivLink) see(chat string, m tgMessage) (isNew bool) {
	return seeSighting(&l.Sightings, chat, m)
}

// seeSighting records a sighting in chat: a chat keeps only its oldest message with the link,
// and the list stays sorted by chat key. It reports whether the list was empty before, i.e.
// the link was seen in no chat at all.
func seeSighting(list *[]tgSighting, chat string, m tgMessage) (isNew bool) {
	isNew = len(*list) == 0
	s := tgSighting{Chat: chat, MessageID: m.ID, Topic: m.Topic, Date: tgDate(m.Date)}
	for i := range *list {
		if (*list)[i].Chat != chat {
			continue
		}
		if m.ID < (*list)[i].MessageID {
			(*list)[i] = s
		}
		return isNew
	}
	*list = append(*list, s)
	sort.Slice(*list, func(i, j int) bool { return (*list)[i].Chat < (*list)[j].Chat })
	return isNew
}

// earlierSighting orders sightings by date (an unknown date last), then by chat key.
func earlierSighting(a, b tgSighting) bool {
	if a.Date != b.Date {
		if a.Date == "" {
			return false
		}
		if b.Date == "" {
			return true
		}
		return a.Date < b.Date // RFC3339 in UTC sorts as text
	}
	return a.Chat < b.Chat
}

type tgExport struct {
	UpdatedAt  string                  `json:"updated_at"`
	Chats      map[string]*tgChatState `json:"chats"`
	PixivUsers map[string]*tgPixivUser `json:"pixiv_users"`
	PixivPosts map[string]*tgPixivPost `json:"pixiv_posts"`

	// links, when set, also collects the file-host links of the scanned messages (tgfiles.go).
	// It is saved in its own file, so it is not part of this one.
	links *tgLinks
}

func (e *tgExport) init() {
	if e.Chats == nil {
		e.Chats = map[string]*tgChatState{}
	}
	if e.PixivUsers == nil {
		e.PixivUsers = map[string]*tgPixivUser{}
	}
	if e.PixivPosts == nil {
		e.PixivPosts = map[string]*tgPixivPost{}
	}
}

// tgMessage is what the scan needs of a Telegram message.
type tgMessage struct {
	ID    int
	Date  int      // unix time
	Topic int      // forum topic id (1 = General, also for chats without topics); 0 for service messages
	Texts []string // text, link entities and button links
}

func tgDate(unix int) string {
	if unix <= 0 {
		return ""
	}
	return time.Unix(int64(unix), 0).UTC().Format(time.RFC3339)
}

// record adds the pixiv links of one message and returns how many users and posts are new.
// Pixiv posts only count when postTopic is 0 or the message is in that topic.
func (e *tgExport) record(chat string, m tgMessage, postTopic int) (newUsers, newPosts int) {
	if e.links != nil {
		e.links.add(chat, m) // file-host links are not limited to post_topic
	}
	users, posts := extractPixiv(m.Texts...)
	for _, id := range users {
		l := e.PixivUsers[id]
		if l == nil {
			l = &tgPixivLink{ID: id, URL: pixivUserLink(id)}
			e.PixivUsers[id] = l
		}
		if l.see(chat, m) {
			newUsers++
		}
	}
	if postTopic != 0 && m.Topic != postTopic {
		return newUsers, 0
	}
	for _, id := range posts {
		l := e.PixivPosts[id]
		if l == nil {
			l = &tgPixivLink{ID: id, URL: pixivPostLink(id)}
			e.PixivPosts[id] = l
		}
		if l.see(chat, m) {
			newPosts++
		}
	}
	return newUsers, newPosts
}

// scan reads messages (newest first) until it reaches the last message of the previous export,
// and records their pixiv links. The chat's last_message_id moves forward only when the scan
// finished, so an interrupted export is simply read again next run (links already found stay).
// Changing a chat's post_topic starts that chat over, as the old export skipped other topics.
func (e *tgExport) scan(chat string, cfg tgChatCfg, st *tgChatState, msgs iter.Seq2[tgMessage, error]) (scanned, newUsers, newPosts int, err error) {
	pixivPrev := st.LastMessageID
	if st.PostTopic != cfg.PostTopic {
		pixivPrev = 0
	}
	prev, newest := pixivPrev, pixivPrev
	if e.links != nil {
		// the links file has its own position: read from the older one, so a links file that
		// was deleted (or is new) is filled again without re-reading anything twice otherwise
		linkPrev := 0
		if lc := e.links.Chats[chat]; lc != nil {
			linkPrev = lc.LastMessageID
		}
		prev = min(prev, linkPrev)
		newest = max(newest, linkPrev)
	}
	for m, merr := range msgs {
		if merr != nil {
			return scanned, newUsers, newPosts, merr
		}
		if m.ID <= prev {
			break // reached what the previous export already had
		}
		if m.ID > newest {
			newest = m.ID
		}
		scanned++
		u, p := e.record(chat, m, cfg.PostTopic)
		newUsers += u
		newPosts += p
	}
	st.LastMessageID = newest
	if e.links != nil {
		e.links.Chats[chat] = &tgLinkChat{LastMessageID: newest, UpdatedAt: time.Now().UTC().Format(time.RFC3339)}
	}
	st.PostTopic = cfg.PostTopic
	st.Link = cfg.Link
	st.Scanned += scanned
	st.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	return scanned, newUsers, newPosts, nil
}

// scanChat is scan plus the bookkeeping of the chat's state entry.
func (e *tgExport) scanChat(key string, cfg tgChatCfg, msgs iter.Seq2[tgMessage, error]) (scanned, newUsers, newPosts int, err error) {
	st := e.Chats[key]
	if st == nil {
		st = &tgChatState{}
		e.Chats[key] = st
	}
	st.NoLookup = cfg.NoLookup
	if cfg.order > 0 {
		st.Order = cfg.order
	}
	return e.scan(key, cfg, st, msgs)
}
