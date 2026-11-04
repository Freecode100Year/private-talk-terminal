package server

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// Tor exit relays are shared by many people. Counting them per address would
// let only a handful of Tor Browser users in at a time (3 sessions, 10
// connects a minute per address), so connections from a known exit share the
// generous Tor bucket that onion-service users already use. (devzat keeps the
// same list to *block* Tor users; we do the opposite.)
//
// The list is Tor Project's public bulk exit list. Fetching it reveals
// nothing about users.

const torExitListURL = "https://check.torproject.org/torbulkexitlist"

type exitSet = map[string]struct{}

// isTorExit reports whether ip (no port) is a known Tor exit relay.
func (s *Server) isTorExit(ip string) bool {
	m := s.torExits.Load()
	if m == nil {
		return false
	}
	_, ok := (*m)[ip]
	return ok
}

// setTorExits replaces the known exit list.
func (s *Server) setTorExits(ips []string) {
	m := make(exitSet, len(ips))
	for _, ip := range ips {
		if p := net.ParseIP(strings.TrimSpace(ip)); p != nil {
			m[p.String()] = struct{}{}
		}
	}
	s.torExits.Store(&m)
}

func parseExitList(r io.Reader) []string {
	var out []string
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if net.ParseIP(line) != nil {
			out = append(out, line)
		}
	}
	return out
}

// refreshTorExits downloads the list now and then every 6 hours. Failures
// keep the previous list (or none), so it never blocks anyone.
func (s *Server) refreshTorExits(ctx context.Context) {
	client := &http.Client{Timeout: 30 * time.Second}
	fetch := func() {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, torExitListURL, nil)
		req.Header.Set("User-Agent", "private-talk-terminal")
		resp, err := client.Do(req)
		if err != nil {
			s.log.Warn("tor exit list: fetch failed", "err", err)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			s.log.Warn("tor exit list: unexpected status", "status", resp.StatusCode)
			return
		}
		ips := parseExitList(io.LimitReader(resp.Body, 4<<20))
		if len(ips) < 100 { // implausibly short: keep what we had
			s.log.Warn("tor exit list: too short, ignored", "count", len(ips))
			return
		}
		s.setTorExits(ips)
		s.log.Info("tor exit list updated", "count", len(ips))
	}
	fetch()
	t := time.NewTicker(6 * time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			fetch()
		}
	}
}
