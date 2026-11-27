# Telegram chat export

The scraper can read Telegram chats and collect every **pixiv user link** and **pixiv post link**
posted in them. The Telegram login and client use [gotd/td](https://github.com/gotd/td); the few pieces taken
from [tdl](https://github.com/iyear/tdl) (AGPL-3.0) live in `internal/tgcore`, so tdl itself is not a dependency.

## Setup

1. List the chats in `config/telegram_chats.json` (see `config/telegram_chats.example.json`):

   ```json
   {
     "chats": [
       "https://t.me/some_public_channel",
       { "link": "https://t.me/c/1234567890", "post_topic": 42 }
     ]
   }
   ```

   - `link`: `https://t.me/name`, `@name`, `https://t.me/c/<id>` or the numeric chat id.
     Invite links (`t.me/+...`) cannot be used; use the `t.me/c/<id>` link of a chat you have joined.
   - `post_topic` (optional): a forum topic id. Pixiv **user** links are always taken from the whole
     chat; pixiv **post** links only from messages in that topic. Without it, posts come from the
     whole chat. The topic id is the first number after the chat id in a topic link
     (`https://t.me/c/<chat>/<topic>`).

2. Put the credentials in `.env` (see `.env.example`): `TELEGRAM_PHONE`, and `TELEGRAM_PASSWORD`
   if the account has 2FA. `TELEGRAM_API_ID` / `TELEGRAM_API_HASH` are optional (your own app from
   my.telegram.org; otherwise the Telegram Desktop app is used).

3. Run:

   ```
   go run . -telegram-only
   ```

   The first run asks for the login code on the terminal (or reads `TELEGRAM_CODE`). The session is
   saved in `telegram_session/` (gitignored), so later runs log in by themselves. To reuse an
   existing `tdl login`, run with `-telegram-session ~/.tdl/data`.

Without `-telegram-only`, the export runs first in a normal scrape, but only when
`config/telegram_chats.json` exists; a Telegram failure is logged and the scrape goes on.
`-no-telegram` skips it.

   - `no_lookup` (optional, `true`): keep this chat's messages out of the lookup file (see below).

## Output

`data/telegram_pixiv.json`:

- `chats[<key>]`: `last_message_id` of the last complete export, `messages_scanned`, `title`.
- `pixiv_users[<pixiv id>]` and `pixiv_posts[<artwork id>]`: canonical URL, plus the chat, message id
  (and topic for posts) where it was first seen.

## Incremental exports

Messages are read newest first and reading stops at the `last_message_id` of the previous export, so
a rerun only reads new messages. The id is only moved forward when a chat finished; an interrupted
export is read again next time (links already found are kept). Changing a chat's `post_topic`
re-reads that chat from the start. Edits to messages older than the last export are not seen.
To force a full re-read, delete the chat's entry under `chats` in `data/telegram_pixiv.json`.

## Browser extension (lookup.json)

`lookup.json` for the KKAFIO Link Collector extension gets a `telegram` section (`go run . -lookup-only -lookup <path>`
reads `data/telegram_pixiv.json` for it): `posts` and `users` map a pixiv id to the path of the first Telegram message
that links it (`name/123`, `c/1234567890/123`, or `name/42/123` inside forum topic 42); the link is `https://t.me/<path>`.
The extension then shows a **TG** badge on pixiv thumbnails of such posts and a Telegram chip on artwork and artist pages.

`lookup.json` is published on GitHub Pages, so every chat that is not marked `"no_lookup": true` is public there:
mark private chats (their `t.me/c/...` links only open for members, and show the chat id).

## Authors

Every pixiv user found in `data/telegram_pixiv.json` is treated like an account found on kknow.cc
(`kknow.go`) in a normal run (without `-telegram-only`, which only exports):

1. the account's pixiv profile is read (name, description, links) into `cache/pixiv/profiles.json`;
2. once the profile has a name, the account becomes an entry of `data/authors.json` named after the
   profile, with its pixiv link, and gets the pawchive, bepis, download and telegram-hashtag matches
   the other rows get. An account already in `authors.json` or belonging to a koikatsucards author is
   skipped, and one whose profile name is the name or alias of an entry that has no pixiv account yet
   joins that entry instead of getting a second row. Deleted or nameless accounts are retried next run.

Typical use: `go run .` (exports new Telegram messages, reads the new profiles, updates authors.json).
Pixiv post links are only stored in `telegram_pixiv.json`; they are not used for authors.
