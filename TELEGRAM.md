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
- `pixiv_users[<pixiv id>]` and `pixiv_posts[<artwork id>]`: canonical URL and `sightings`, one per chat
  that has the link: the chat, the oldest message of that chat with it (message id, topic, date).

`data/telegram_links.json` (`-telegram-links` to move it) holds the file-host links found in the same messages, kept in
a separate file because there can be thousands of them and nothing else reads them:

- `links[<url>]`: `url`, `service` and `sightings` (one per chat, the oldest message of that chat with the link:
  chat, message id, topic, date; a message link is `https://t.me/<chat>/<message id>` or `t.me/c/<id>/<message id>`).
- `chats[<key>]`: the `last_message_id` of this file. It is separate from the pixiv file's, so either file can be
  deleted and rebuilt alone; a rerun reads from the older of the two positions.

Recognized hosts (and their subdomains): mega.nz / mega.co.nz / mega.io (`mega`), gofile.io (`gofile`),
catbox.moe (`catbox`), mediafire.com (`mediafire`), pixeldrain.com / .net (`pixeldrain`), dropbox.com and
dropboxusercontent.com (`dropbox`), drive.google.com and drive.usercontent.google.com (`gdrive`). Links come from the
text, hidden text links and URL buttons, from every topic of a chat (`post_topic` only limits pixiv posts). The
URL is saved as written after the host and path (https, lower-case host, trailing punctuation removed, `#key` of a
mega link kept); two spellings of the same file are not merged. A mega key posted in a separate message is not
joined to its link. Google Docs/Sheets links (docs.google.com) are not included.

The links of `no_lookup` chats are saved here too (this file is not used for `lookup.json`). Like the other
files in `data/` it is committed by the scrape workflow, so mind who can read the repository.

## Incremental exports

Messages are read newest first and reading stops at the `last_message_id` of the previous export, so
a rerun only reads new messages. The id is only moved forward when a chat finished; an interrupted
export is read again next time (links already found are kept). Changing a chat's `post_topic`
re-reads that chat from the start. Edits to messages older than the last export are not seen.
To force a full re-read, delete the chat's entry under `chats` in `data/telegram_pixiv.json`.

## Browser extension (lookup.json)

`lookup.json` for the KKAFIO Link Collector extension gets a `telegram` section (`go run . -lookup-only -lookup <path>`
reads `data/telegram_pixiv.json` for it): `posts` and `users` map a pixiv id to the path of a Telegram message
that links it. Chats marked `no_lookup` are skipped; of the others a post takes the chat that comes first in
`telegram_chats.json` (saved as `order` in each chat's state) and a user the earliest message. The path looks like `name/123`, `c/1234567890/123`, or `name/42/123` inside forum topic 42, and the link is `https://t.me/<path>`.
The extension then shows a **TG** badge on pixiv thumbnails of such posts and a Telegram chip on artwork and artist pages.

`lookup.json` is published on GitHub Pages, so every chat that is not marked `"no_lookup": true` is public there:
mark private chats (their `t.me/c/...` links only open for members, and show the chat id). A link seen in a
private and a public chat points at the public one; one seen only in private chats is left out.

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

## Running in CI

A login code cannot be typed in a workflow, so log in once on your machine and give CI the saved session:

1. Run `go run . -telegram-only` locally and enter the login code. This also fills the peer cache
   in the session, which `t.me/c/<id>` chats need.
2. Encode the session (compressed, to stay under GitHub's 48 KB secret limit):

   ```
   tar czf - telegram_session | base64 -w0
   ```

3. Save the output as the repository secret `TELEGRAM_SESSION`.

`.github/workflows/scrape.yml` restores it before the scraper runs. Without the secret the telegram
step fails with "not logged in", which is logged and the scrape goes on. The session is only read,
never written back; `data/telegram_pixiv.json` is committed, so each run reads only new messages.

Use a dedicated Telegram account: a session used from a datacenter IP can get flagged or logged
out, and anyone who can read the secret controls the account. If the session dies, repeat the
steps above.
