// Package tgcore is the small part of github.com/iyear/tdl (core/storage, core/tclient,
// core/middlewares, core/util/tutil, pkg/kv, pkg/tclient, pkg/key) that the Telegram source
// needs, copied here so that tdl itself is no longer a dependency.
//
// tdl is licensed under the GNU AGPL-3.0 (Copyright (c) iyear), like this project. The code
// is trimmed: no NTP clock, DC overrides, update handler, takeout, legacy/file key-value
// drivers or logging. The on-disk session layout is unchanged (a bbolt file "default" with a
// bucket "default" inside the session directory), so a session made by "tdl login" (default
// ~/.tdl/data) or by an earlier version of this scraper keeps working.
package tgcore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/gotd/contrib/middleware/floodwait"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/dcs"
	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"github.com/iyear/connectproxy"
	"go.etcd.io/bbolt"
	"golang.org/x/net/proxy"
)

// ---- key-value storage (tdl pkg/kv bolt driver, core/storage) ----

// ErrNotFound is returned by Storage.Get for a missing key.
var ErrNotFound = errors.New("key not found")

// Storage is a namespaced key-value store.
type Storage interface {
	Get(ctx context.Context, key string) ([]byte, error)
	Set(ctx context.Context, key string, value []byte) error
	Delete(ctx context.Context, key string) error
}

const namespace = "default"

var boltOptions = &bbolt.Options{
	Timeout:      time.Second,
	NoGrowSync:   false,
	FreelistType: bbolt.FreelistArrayType,
}

// DB is an opened session directory.
type DB struct {
	db *bbolt.DB
}

// Open opens (creating it when needed) the session storage in dir and returns it with its
// default namespace.
func Open(dir string) (*DB, Storage, error) {
	if dir == "" {
		return nil, nil, errors.New("session directory is required")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, nil, fmt.Errorf("create dir: %w", err)
	}
	db, err := bbolt.Open(filepath.Join(dir, namespace), os.ModePerm, boltOptions)
	if err != nil {
		return nil, nil, fmt.Errorf("open db: %w", err)
	}
	if err := db.Update(func(tx *bbolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists([]byte(namespace))
		return err
	}); err != nil {
		db.Close()
		return nil, nil, fmt.Errorf("create bucket: %w", err)
	}
	return &DB{db: db}, &bucket{db: db, ns: []byte(namespace)}, nil
}

// Close closes the session storage.
func (d *DB) Close() error { return d.db.Close() }

type bucket struct {
	db *bbolt.DB
	ns []byte
}

func (b *bucket) Get(_ context.Context, key string) ([]byte, error) {
	var val []byte
	if err := b.db.View(func(tx *bbolt.Tx) error {
		if v := tx.Bucket(b.ns).Get([]byte(key)); v != nil {
			val = make([]byte, len(v)) // bbolt values are only valid inside the transaction
			copy(val, v)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	if val == nil {
		return nil, ErrNotFound
	}
	return val, nil
}

func (b *bucket) Set(_ context.Context, key string, value []byte) error {
	return b.db.Update(func(tx *bbolt.Tx) error {
		return tx.Bucket(b.ns).Put([]byte(key), value)
	})
}

func (b *bucket) Delete(_ context.Context, key string) error {
	return b.db.Update(func(tx *bbolt.Tx) error {
		return tx.Bucket(b.ns).Delete([]byte(key))
	})
}

// ---- gotd session and peer storage (tdl core/storage) ----

const (
	sessionKey = "session"
	appKey     = "app"
)

type session struct{ kv Storage }

// NewSession is the gotd session storage on top of kv.
func NewSession(kv Storage) telegram.SessionStorage { return session{kv: kv} }

func (s session) LoadSession(ctx context.Context) ([]byte, error) {
	b, err := s.kv.Get(ctx, sessionKey)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	return b, err
}

func (s session) StoreSession(ctx context.Context, data []byte) error {
	return s.kv.Set(ctx, sessionKey, data)
}

type peerStore struct{ kv Storage }

// NewPeers is the gotd peers storage on top of kv.
func NewPeers(kv Storage) peers.Storage { return peerStore{kv: kv} }

func peerKey(k peers.Key) string   { return "peers:key:" + k.Prefix + ":" + strconv.FormatInt(k.ID, 10) }
func phoneKey(phone string) string { return "peers:phone:" + phone }

const contactsKey = "peers:contacts:hash"

func (p peerStore) Save(ctx context.Context, k peers.Key, v peers.Value) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return p.kv.Set(ctx, peerKey(k), b)
}

func (p peerStore) Find(ctx context.Context, k peers.Key) (peers.Value, bool, error) {
	data, err := p.kv.Get(ctx, peerKey(k))
	if errors.Is(err, ErrNotFound) {
		return peers.Value{}, false, nil
	}
	if err != nil {
		return peers.Value{}, false, err
	}
	var v peers.Value
	if err := json.Unmarshal(data, &v); err != nil {
		return peers.Value{}, false, err
	}
	return v, true, nil
}

func (p peerStore) SavePhone(ctx context.Context, phone string, k peers.Key) error {
	b, err := json.Marshal(k)
	if err != nil {
		return err
	}
	return p.kv.Set(ctx, phoneKey(phone), b)
}

func (p peerStore) FindPhone(ctx context.Context, phone string) (peers.Key, peers.Value, bool, error) {
	data, err := p.kv.Get(ctx, phoneKey(phone))
	if errors.Is(err, ErrNotFound) {
		return peers.Key{}, peers.Value{}, false, nil
	}
	if err != nil {
		return peers.Key{}, peers.Value{}, false, err
	}
	var k peers.Key
	if err := json.Unmarshal(data, &k); err != nil {
		return peers.Key{}, peers.Value{}, false, err
	}
	v, found, err := p.Find(ctx, k)
	if err != nil {
		return peers.Key{}, peers.Value{}, false, err
	}
	return k, v, found, nil
}

func (p peerStore) GetContactsHash(ctx context.Context) (int64, error) {
	data, err := p.kv.Get(ctx, contactsKey)
	if errors.Is(err, ErrNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(string(data), 10, 64)
}

func (p peerStore) SaveContactsHash(ctx context.Context, hash int64) error {
	return p.kv.Set(ctx, contactsKey, []byte(strconv.FormatInt(hash, 10)))
}

// ---- Telegram apps (tdl pkg/tclient, pkg/key) ----

const (
	AppBuiltin = "builtin"
	AppDesktop = "desktop"
)

// App is a Telegram API application (id and hash).
type App struct {
	AppID   int
	AppHash string
}

var apps = map[string]App{
	// application created by iyear (tdl)
	AppBuiltin: {AppID: 15055931, AppHash: "021d433426cbb920eeb95164498fe3d3"},
	// application created by tdesktop
	AppDesktop: {AppID: 2040, AppHash: "b18441a1ff607e10a989891a5462e627"},
}

// EnsureApp stores the desktop app in kv when the session has no app yet.
func EnsureApp(ctx context.Context, kv Storage) error {
	if _, err := kv.Get(ctx, appKey); err != nil {
		return kv.Set(ctx, appKey, []byte(AppDesktop))
	}
	return nil
}

// GetApp is the app stored in kv (the builtin one when none is stored).
func GetApp(kv Storage) (App, error) {
	mode, err := kv.Get(context.TODO(), appKey)
	if err != nil {
		mode = []byte(AppBuiltin)
	}
	app, ok := apps[string(mode)]
	if !ok {
		return App{}, fmt.Errorf("can't find app: %s, please try re-login", mode)
	}
	return app, nil
}

// ---- client (tdl core/tclient, core/middlewares, core/util) ----

// Device is the device the client presents itself as.
var device = telegram.DeviceConfig{
	DeviceModel:    "Desktop",
	SystemVersion:  "Windows 10",
	AppVersion:     "4.2.4 x64",
	LangCode:       "en",
	SystemLangCode: "en-US",
	LangPack:       "tdesktop",
}

// Options configure NewClient.
type Options struct {
	AppID            int
	AppHash          string
	Session          telegram.SessionStorage
	Middlewares      []telegram.Middleware
	Proxy            string // protocol://user:pass@host:port, optional
	ReconnectTimeout time.Duration
}

// NewClient creates a Telegram client. The retry, connection recovery and flood wait
// middlewares are always added, in front of o.Middlewares.
func NewClient(ctx context.Context, o Options) (*telegram.Client, error) {
	var dialer dcs.DialFunc = proxy.Direct.DialContext
	if o.Proxy != "" {
		d, err := newProxy(o.Proxy)
		if err != nil {
			return nil, fmt.Errorf("get dialer: %w", err)
		}
		dialer = d.DialContext
	}
	return telegram.NewClient(o.AppID, o.AppHash, telegram.Options{
		Resolver: dcs.Plain(dcs.PlainOptions{Dial: dialer}),
		ReconnectionBackoff: func() backoff.BackOff {
			return newBackoff(o.ReconnectTimeout)
		},
		Device:         device,
		SessionStorage: o.Session,
		RetryInterval:  5 * time.Second,
		MaxRetries:     5,
		DialTimeout:    10 * time.Second,
		Middlewares: append([]telegram.Middleware{
			newRecovery(ctx, newBackoff(o.ReconnectTimeout)),
			newRetry(5),
			floodwait.NewSimpleWaiter(),
		}, o.Middlewares...),
	}), nil
}

func newBackoff(timeout time.Duration) backoff.BackOff {
	b := backoff.NewExponentialBackOff()
	b.Multiplier = 1.1
	b.MaxElapsedTime = timeout
	b.MaxInterval = 10 * time.Second
	return b
}

func init() {
	// lets proxy.FromURL understand http:// and https:// CONNECT proxies
	connectproxy.Register(&connectproxy.Config{InsecureSkipVerify: true})
}

func newProxy(proxyURL string) (proxy.ContextDialer, error) {
	u, err := url.Parse(proxyURL)
	if err != nil {
		return nil, fmt.Errorf("parse proxy url: %w", err)
	}
	dialer, err := proxy.FromURL(u, proxy.Direct)
	if err != nil {
		return nil, fmt.Errorf("proxy from url: %w", err)
	}
	if d, ok := dialer.(proxy.ContextDialer); ok {
		return d, nil
	}
	return nil, errors.New("proxy dialer is not ContextDialer")
}

// recovery retries a request, with backoff, while the connection is down (any error that is
// not a Telegram error), until ctx or the client's context is done.
type recovery struct {
	ctx     context.Context
	backoff backoff.BackOff
}

func newRecovery(ctx context.Context, b backoff.BackOff) telegram.Middleware {
	return &recovery{ctx: ctx, backoff: b}
}

func (r *recovery) Handle(next tg.Invoker) telegram.InvokeFunc {
	return func(ctx context.Context, input bin.Encoder, output bin.Decoder) error {
		return backoff.Retry(func() error {
			if err := next.Invoke(ctx, input, output); err != nil {
				if r.shouldRecover(ctx, err) {
					return fmt.Errorf("recover: %w", err)
				}
				return backoff.Permanent(err)
			}
			return nil
		}, r.backoff)
	}
}

func (r *recovery) shouldRecover(ctx context.Context, err error) bool {
	// stop when interrupted (ctrl+c) instead of waiting for the backoff to run out
	select {
	case <-r.ctx.Done():
		return false
	case <-ctx.Done():
		return false
	default:
	}
	_, ok := tgerr.As(err)
	return !ok
}

// retry repeats a request that failed with one of Telegram's transient internal errors.
var internalErrors = []string{
	"Timedout",
	"No workers running",
	"RPC_CALL_FAIL",
	"RPC_MCGET_FAIL",
	"WORKER_BUSY_TOO_LONG_RETRY",
	"memory limit exit",
}

type retry struct{ max int }

func newRetry(max int) telegram.Middleware { return retry{max: max} }

func (r retry) Handle(next tg.Invoker) telegram.InvokeFunc {
	return func(ctx context.Context, input bin.Encoder, output bin.Decoder) error {
		for retries := 0; retries < r.max; retries++ {
			err := next.Invoke(ctx, input, output)
			if err == nil {
				return nil
			}
			if tgerr.Is(err, internalErrors...) {
				continue
			}
			return fmt.Errorf("retry middleware skip: %w", err)
		}
		return fmt.Errorf("retry limit reached after %d attempts", r.max)
	}
}

// ---- peer resolution (tdl core/util/tutil) ----

// GetInputPeer resolves a chat from a username or a numeric id (channel, user or basic
// group id).
func GetInputPeer(ctx context.Context, manager *peers.Manager, from string) (peers.Peer, error) {
	id, err := strconv.ParseInt(from, 10, 64)
	if err != nil {
		// from is a username
		return manager.Resolve(ctx, from)
	}
	var p peers.Peer
	if p, err = manager.ResolveChannelID(ctx, id); err == nil {
		return p, nil
	}
	if p, err = manager.ResolveUserID(ctx, id); err == nil {
		return p, nil
	}
	if p, err = manager.ResolveChatID(ctx, id); err == nil {
		return p, nil
	}
	return nil, fmt.Errorf("failed to get result from %d: %v", id, err)
}
