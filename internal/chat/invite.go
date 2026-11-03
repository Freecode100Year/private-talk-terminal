package chat

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"regexp"
	"strings"
)

// End-to-end encrypted rooms are delegated to HTML Room
// (github.com/Freecode100Year/htmlroom): the room key is created in the
// user's own browser and lives in the link's #fragment. This server never
// creates or handles keys — it only recognises invite links so it can
//
//   - refuse links WITHOUT a passphrase: such a link *is* the key, and
//     everything typed here is visible to the server and its operator;
//   - present passphrase-protected links as an invite, clickable even when
//     the terminal would otherwise wrap the long URL.
//
// Format (htmlroom html-room/transport.js): https://host/[hr.html]#hr1=<base64url(JSON config)>.
// A config with "master" carries the raw key; "encrypted":true means the key
// is wrapped with a passphrase (PBKDF2-SHA-256, 310000 rounds).

// MaxInviteRunes bounds a message that carries an invite link (htmlroom
// itself rejects fragments over 8192 characters).
const MaxInviteRunes = 9000

var inviteRe = regexp.MustCompile(`https://[A-Za-z0-9.\-]+(?::[0-9]{1,5})?(?:/[A-Za-z0-9._~\-/]*)?#hr1=[A-Za-z0-9_-]{16,}`)

// Invite is a recognised HTML Room link.
type Invite struct {
	URL       string // the full link
	Host      string // where it opens
	Title     string // room title from the config, if any (sanitised)
	Encrypted bool   // key wrapped with a passphrase
	Trusted   bool   // opens on the server's configured HTML Room site
	N         int    // invite number, for /link
}

// FindInvite returns the first HTML Room link in text, or nil. ok is false
// when text contains something that looks like an invite but can't be
// decoded (treated as a broken link).
func FindInvite(text string) (inv *Invite, ok bool) {
	if !strings.Contains(text, "#hr1=") { // fast path for ordinary messages
		return nil, true
	}
	loc := inviteRe.FindStringIndex(text)
	if loc == nil {
		return nil, false
	}
	raw := text[loc[0]:loc[1]]
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" {
		return nil, false
	}
	frag := raw[strings.Index(raw, "#hr1=")+5:]
	if len(frag) > 8192 {
		return nil, false
	}
	data, err := base64.RawURLEncoding.DecodeString(frag)
	if err != nil || len(data) > 4096 {
		return nil, false
	}
	var cfg struct {
		Encrypted  *bool   `json:"encrypted"`
		Master     *string `json:"master"`
		Ciphertext *string `json:"ciphertext"`
		Title      string  `json:"title"`
		RoomID     string  `json:"roomId"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil || cfg.RoomID == "" {
		return nil, false
	}
	inv = &Invite{URL: raw, Host: u.Host, Title: Sanitize(cfg.Title, 60)}
	// Only call it protected when it really is: wrapped key present and no
	// raw key anywhere in the config.
	inv.Encrypted = cfg.Encrypted != nil && *cfg.Encrypted && cfg.Master == nil && cfg.Ciphertext != nil
	return inv, true
}
