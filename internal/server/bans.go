package server

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// Ban persistence (opt-in, -ban-file).
//
// Bans are keyed by an HMAC of the client address. By default the HMAC key
// lives only in memory and changes on every restart, so nothing on disk can
// ever be linked to an address — but bans are forgotten on restart too.
// With a ban file the HMAC key is kept next to it (mode 0600) so bans
// survive restarts and upgrades. Trade-off, documented in the README: anyone
// who obtains BOTH files could brute-force which IPv4 addresses are banned
// (only banned addresses are ever written; nothing about other users is).

type banFile struct {
	Version int                  `json:"version"`
	Bans    map[string]time.Time `json:"bans"`
}

// loadOrCreateSalt reads the persistent HMAC key, creating it if missing.
func loadOrCreateSalt(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err == nil {
		if len(b) != 32 {
			return nil, fmt.Errorf("%s: unexpected length %d", path, len(b))
		}
		return b, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	b = make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	if err := writeFileAtomic(path, b); err != nil {
		return nil, err
	}
	return b, nil
}

func readBans(path string) (map[string]time.Time, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f banFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return f.Bans, nil
}

func writeBans(path string, bans map[string]time.Time) error {
	data, err := json.MarshalIndent(banFile{Version: 1, Bans: bans}, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(data, '\n'))
}

// writeFileAtomic writes via a temp file + rename so a crash never leaves a
// truncated file behind.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// persistBans saves the hub's bans whenever they change, and once more on
// shutdown.
func (s *Server) persistBans(ctx context.Context) {
	if s.banFile == "" {
		return
	}
	var saved uint64
	save := func() {
		bans, ver := s.hub.Bans()
		if ver == saved {
			return
		}
		if err := writeBans(s.banFile, bans); err != nil {
			s.log.Error("saving bans failed", "err", err)
			return
		}
		saved = ver
	}
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			save()
			return
		case <-t.C:
			save()
		}
	}
}
