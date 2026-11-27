package main

// The Telegram part of the telegram source (see tgsource.go), built on the tdl packages
// (github.com/iyear/tdl, a normal Go dependency): its session storage, client setup (flood waits, retries, proxy) and peer
// resolution. Messages are read with the same gotd history query tdl's "chat export" uses.

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"iter"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/gotd/contrib/middleware/ratelimit"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/telegram/query"
	"github.com/gotd/td/tg"
	"golang.org/x/time/rate"

	"github.com/iyear/tdl/core/storage"
	tclientcore "github.com/iyear/tdl/core/tclient"
	"github.com/iyear/tdl/core/util/tutil"
	"github.com/iyear/tdl/pkg/key"
	"github.com/iyear/tdl/pkg/kv"
	tclient "github.com/iyear/tdl/pkg/tclient"
)

// Credentials come from the environment (the .env file is loaded into it):
//
//	TELEGRAM_PHONE     phone number, needed only for the first login
//	TELEGRAM_PASSWORD  2FA password, if the account has one
//	TELEGRAM_CODE      login code, optional: asked on the terminal when missing
//	TELEGRAM_API_ID, TELEGRAM_API_HASH   your own app from my.telegram.org, optional
//	                   (default: the app tdl uses, as "tdl login -T code" does)
//	TELEGRAM_PROXY     optional, protocol://user:pass@host:port
//	TELEGRAM_SESSION_DIR  where the login session is kept (default telegram_session)
//
// The session is saved after the first login, so later runs need no code.

const tgDefaultSessionDir = "telegram_session"

var tgLimiter = ratelimit.New(rate.Every(500*time.Millisecond), 2) // same as tdl's own commands

type tgOptions struct {
	ChatsPath  string
	OutPath    string
	SessionDir string
}

// telegramStep runs the telegram source with the defaults filled in. When the chat list does
// not exist the step is skipped, unless only is set (then it is an error).
func telegramStep(dataDir, chatsPath, outPath, sessionDir string, only bool) error {
	if chatsPath == "" {
		chatsPath = filepath.Join(dataDir, "telegram_chats.json")
	}
	if outPath == "" {
		outPath = filepath.Join(dataDir, "telegram_pixiv.json")
	}
	if sessionDir == "" {
		sessionDir = os.Getenv("TELEGRAM_SESSION_DIR")
	}
	if sessionDir == "" {
		sessionDir = tgDefaultSessionDir
	}
	if _, err := os.Stat(chatsPath); errors.Is(err, os.ErrNotExist) {
		if only {
			return fmt.Errorf("telegram chat list %s not found (see data/telegram_chats.example.json)", chatsPath)
		}
		return nil
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runTelegram(ctx, tgOptions{ChatsPath: chatsPath, OutPath: outPath, SessionDir: sessionDir})
}

func runTelegram(ctx context.Context, o tgOptions) (err error) {
	chats, err := loadTelegramChats(o.ChatsPath)
	if err != nil {
		return err
	}
	if len(chats) == 0 {
		log.Printf("telegram: %s lists no chats", o.ChatsPath)
		return nil
	}
	exp := &tgExport{}
	if err := loadJSON(o.OutPath, exp); err != nil {
		return fmt.Errorf("loading %s: %w", o.OutPath, err)
	}
	exp.init()
	save := func() error {
		exp.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		return saveJSON(o.OutPath, exp)
	}
	defer func() {
		if serr := save(); serr != nil {
			err = errors.Join(err, serr)
		}
	}()

	var failed []error
	err = tgRun(ctx, o.SessionDir, func(ctx context.Context, api *tg.Client, manager *peers.Manager) error {
		for _, c := range chats {
			ident, ck, _ := parseTelegramChat(c.Link) // validated by loadTelegramChats
			peer, perr := tutil.GetInputPeer(ctx, manager, ident)
			if perr != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				log.Printf("telegram: %s: cannot open chat: %v", c.Link, perr)
				failed = append(failed, fmt.Errorf("%s: %w", c.Link, perr))
				continue
			}
			prev := 0
			if st := exp.Chats[ck]; st != nil && st.PostTopic == c.PostTopic {
				prev = st.LastMessageID
			}
			log.Printf("telegram: %s (%s), reading messages newer than #%d", c.Link, peer.VisibleName(), prev)
			n, nu, np, serr := exp.scanChat(ck, c, tgHistory(ctx, api, peer))
			if st := exp.Chats[ck]; st != nil && peer.VisibleName() != "" {
				st.Title = peer.VisibleName()
			}
			if serr != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				log.Printf("telegram: %s: %v", c.Link, serr)
				failed = append(failed, fmt.Errorf("%s: %w", c.Link, serr))
				continue
			}
			log.Printf("telegram: %s: %d new messages, %d new pixiv users, %d new pixiv posts", c.Link, n, nu, np)
			if err := save(); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	log.Printf("telegram: %d pixiv users and %d pixiv posts in %s", len(exp.PixivUsers), len(exp.PixivPosts), o.OutPath)
	return errors.Join(failed...)
}

// tgHistory yields the messages of a chat, newest first. The caller (tgExport.scan) stops
// reading by breaking out of the loop, which ends the paging as well.
func tgHistory(ctx context.Context, api *tg.Client, peer peers.Peer) iter.Seq2[tgMessage, error] {
	return func(yield func(tgMessage, error) bool) {
		it := query.NewQuery(api).Messages().GetHistory(peer.InputPeer()).BatchSize(100).Iter()
		count := 0
		for it.Next(ctx) {
			msg := it.Value().Msg
			tm := tgMessage{ID: msg.GetID(), Date: msg.GetDate()}
			if m, ok := msg.(*tg.Message); ok {
				tm.Topic = tgMessageTopic(m)
				tm.Texts = tgMessageTexts(m)
			}
			if !yield(tm, nil) {
				return
			}
			if count++; count%2000 == 0 {
				log.Printf("telegram: %s: %d messages read (at #%d)", peer.VisibleName(), count, tm.ID)
			}
		}
		if err := it.Err(); err != nil {
			yield(tgMessage{}, err)
		}
	}
}

// tgMessageTopic is the forum topic a message belongs to. Messages of the General topic, and
// of chats without topics, have no forum reply header and count as topic 1.
func tgMessageTopic(m *tg.Message) int {
	h, ok := m.ReplyTo.(*tg.MessageReplyHeader)
	if !ok || !h.ForumTopic {
		return 1
	}
	if h.ReplyToTopID != 0 {
		return h.ReplyToTopID // a reply inside the topic
	}
	return h.ReplyToMsgID // a direct message of the topic replies to the topic's first message
}

// tgMessageTexts is every place of a message that can hold a link: the text, the target of
// hidden text links, and inline URL buttons.
func tgMessageTexts(m *tg.Message) []string {
	out := []string{m.Message}
	for _, e := range m.Entities {
		if t, ok := e.(*tg.MessageEntityTextURL); ok {
			out = append(out, t.URL)
		}
	}
	if mk, ok := m.ReplyMarkup.(*tg.ReplyInlineMarkup); ok {
		for _, row := range mk.Rows {
			for _, b := range row.Buttons {
				if u, ok := b.(*tg.KeyboardButtonURL); ok {
					out = append(out, u.URL)
				}
			}
		}
	}
	return out
}

// tgRun opens the session storage, connects (logging in from the environment when the session
// has none yet) and runs f with the connected client.
func tgRun(ctx context.Context, sessionDir string, f func(context.Context, *tg.Client, *peers.Manager) error) error {
	stg, err := kv.NewWithMap(map[string]string{
		kv.DriverTypeKey: kv.DriverBolt.String(),
		"path":           sessionDir,
	})
	if err != nil {
		return fmt.Errorf("opening telegram session storage %s: %w", sessionDir, err)
	}
	defer stg.Close()
	kvd, err := stg.Open("default")
	if err != nil {
		return fmt.Errorf("opening telegram session storage %s: %w", sessionDir, err)
	}
	app, err := tgApp(ctx, kvd)
	if err != nil {
		return err
	}
	client, err := tclientcore.New(ctx, tclientcore.Options{
		AppID:            app.AppID,
		AppHash:          app.AppHash,
		Session:          storage.NewSession(kvd, false),
		Middlewares:      []telegram.Middleware{tgLimiter},
		Proxy:            os.Getenv("TELEGRAM_PROXY"),
		ReconnectTimeout: 5 * time.Minute,
	})
	if err != nil {
		return fmt.Errorf("creating telegram client: %w", err)
	}
	return client.Run(ctx, func(ctx context.Context) error {
		status, err := client.Auth().Status(ctx)
		if err != nil {
			return err
		}
		if !status.Authorized {
			if err := tgLogin(ctx, client); err != nil {
				return err
			}
		}
		manager := peers.Options{Storage: storage.NewPeers(kvd)}.Build(client.API())
		return f(ctx, client.API(), manager)
	})
}

// tgApp picks the Telegram app (api id and hash). TELEGRAM_API_ID/TELEGRAM_API_HASH win;
// otherwise the app stored with the session is used, and a new session gets tdl's desktop app,
// as "tdl login -T code" does. Pointing -telegram-session at a tdl data dir (default
// ~/.tdl/data) reuses an existing "tdl login".
func tgApp(ctx context.Context, kvd storage.Storage) (tclient.App, error) {
	if idStr := os.Getenv("TELEGRAM_API_ID"); idStr != "" {
		id, err := strconv.Atoi(strings.TrimSpace(idStr))
		hash := strings.TrimSpace(os.Getenv("TELEGRAM_API_HASH"))
		if err != nil || hash == "" {
			return tclient.App{}, errors.New("TELEGRAM_API_ID must be a number and TELEGRAM_API_HASH must be set too")
		}
		return tclient.App{AppID: id, AppHash: hash}, nil
	}
	if _, err := kvd.Get(ctx, key.App()); err != nil {
		if err := kvd.Set(ctx, key.App(), []byte(tclient.AppDesktop)); err != nil {
			return tclient.App{}, fmt.Errorf("saving telegram app: %w", err)
		}
	}
	return tclient.GetApp(kvd)
}

// tgLogin signs in with TELEGRAM_PHONE / TELEGRAM_PASSWORD; the login code is TELEGRAM_CODE or
// typed on the terminal. The session is stored by the client, so this happens once.
func tgLogin(ctx context.Context, client *telegram.Client) error {
	phone := strings.TrimSpace(os.Getenv("TELEGRAM_PHONE"))
	if phone == "" {
		return errors.New("not logged in to Telegram: set TELEGRAM_PHONE (and TELEGRAM_PASSWORD if the account has 2FA) in .env, then run again to enter the login code")
	}
	code := auth.CodeAuthenticatorFunc(func(ctx context.Context, _ *tg.AuthSentCode) (string, error) {
		if c := strings.TrimSpace(os.Getenv("TELEGRAM_CODE")); c != "" {
			return c, nil
		}
		fmt.Fprint(os.Stderr, "Telegram login code: ")
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && line == "" {
			return "", fmt.Errorf("reading the login code: %w (set TELEGRAM_CODE to give it without a terminal)", err)
		}
		return strings.TrimSpace(line), nil
	})
	flow := auth.NewFlow(auth.Constant(phone, os.Getenv("TELEGRAM_PASSWORD"), code), auth.SendCodeOptions{})
	if err := client.Auth().IfNecessary(ctx, flow); err != nil {
		return fmt.Errorf("telegram login: %w", err)
	}
	log.Println("telegram: logged in; the session is saved")
	return nil
}
