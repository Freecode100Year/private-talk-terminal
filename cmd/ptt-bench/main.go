// Command ptt-bench is a load generator for Private-Talk Terminal. It opens
// N SSH sessions (line mode), has a fraction of them chat at a fixed rate and
// measures how long each message takes to reach every other session.
//
// Run the server with relaxed limits first, e.g.
//
//	ptterm -addr 127.0.0.1:2222 -max-per-ip 100000 -max-tcp-per-ip 100000 -conn-per-min 100000 -max-clients 100000 -msg-rate 100 -msg-burst 100
//	ptt-bench -addr 127.0.0.1:2222 -clients 500 -senders 50 -rate 0.5 -duration 30s
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	gossh "golang.org/x/crypto/ssh"
)

var benchRe = regexp.MustCompile(`BENCH:(\d{19}) s\d+-\d+\D`)

func main() {
	var (
		addr     = flag.String("addr", "127.0.0.1:2222", "server address")
		clients  = flag.Int("clients", 200, "concurrent sessions")
		senders  = flag.Int("senders", 20, "how many of them send messages")
		rate     = flag.Float64("rate", 0.5, "messages per second per sender")
		duration = flag.Duration("duration", 20*time.Second, "how long senders chat")
		ramp     = flag.Duration("ramp", 5*time.Second, "time to open all sessions")
		usePty   = flag.Bool("pty", false, "request a PTY so the server runs the full TUI (latency numbers are then approximate)")
	)
	flag.Parse()
	if *senders > *clients {
		*senders = *clients
	}

	cfg := &gossh.ClientConfig{
		User:            "bench",
		Auth:            []gossh.AuthMethod{gossh.KeyboardInteractive(func(string, string, []string, []bool) ([]string, error) { return nil, nil })},
		HostKeyCallback: gossh.InsecureIgnoreHostKey(),
		Timeout:         20 * time.Second,
	}

	var (
		mu        sync.Mutex
		latencies []time.Duration
		received  atomic.Int64
		sent      atomic.Int64
		failed    atomic.Int64
		ready     sync.WaitGroup
		done      = make(chan struct{})
		writers   = make([]io.Writer, *clients)
		sendStart atomic.Int64 // replayed room history from earlier runs is ignored
	)

	start := time.Now()
	ready.Add(*clients)
	for i := 0; i < *clients; i++ {
		go func(i int) {
			defer ready.Done()
			conn, err := gossh.Dial("tcp", *addr, cfg)
			if err != nil {
				failed.Add(1)
				return
			}
			sess, err := conn.NewSession()
			if err != nil {
				failed.Add(1)
				conn.Close()
				return
			}
			if *usePty {
				if err := sess.RequestPty("xterm-256color", 40, 120, gossh.TerminalModes{}); err != nil {
					failed.Add(1)
					conn.Close()
					return
				}
			}
			in, _ := sess.StdinPipe()
			out, _ := sess.StdoutPipe()
			if err := sess.Shell(); err != nil {
				failed.Add(1)
				conn.Close()
				return
			}
			writers[i] = in
			go func() {
				<-done
				conn.Close()
			}()
			record := func(ns int64) {
				if ns < sendStart.Load() || sendStart.Load() == 0 {
					return
				}
				received.Add(1)
				d := time.Since(time.Unix(0, ns))
				mu.Lock()
				latencies = append(latencies, d)
				mu.Unlock()
			}
			if *usePty {
				// Full-screen output isn't line-oriented (a line is written
				// after its newline), so scan the raw byte stream. Senders'
				// own input echo would be miscounted, so only listeners measure.
				if i < *senders {
					go io.Copy(io.Discard, out)
				} else {
					go func() {
						buf := make([]byte, 32*1024)
						var pending []byte
						for {
							n, err := out.Read(buf)
							pending = append(pending, buf[:n]...)
							for {
								m := benchRe.FindSubmatchIndex(pending)
								if m == nil {
									break
								}
								ns, _ := strconv.ParseInt(string(pending[m[2]:m[3]]), 10, 64)
								record(ns)
								pending = pending[m[1]:]
							}
							if len(pending) > 256 {
								pending = pending[len(pending)-256:]
							}
							if err != nil {
								return
							}
						}
					}()
				}
			} else {
				go func() {
					sc := bufio.NewScanner(out)
					sc.Buffer(make([]byte, 64*1024), 1<<20)
					for sc.Scan() {
						line := sc.Text()
						k := strings.Index(line, "BENCH:")
						if k < 0 {
							continue
						}
						ns, err := strconv.ParseInt(strings.Fields(line[k+6:])[0], 10, 64)
						if err == nil {
							record(ns)
						}
					}
				}()
			}
		}(i)
		time.Sleep(*ramp / time.Duration(*clients))
	}
	ready.Wait()
	connected := *clients - int(failed.Load())
	fmt.Printf("connected %d/%d sessions in %s\n", connected, *clients, time.Since(start).Round(time.Millisecond))
	time.Sleep(2 * time.Second) // let join notices settle

	sendStart.Store(time.Now().UnixNano())
	interval := time.Duration(float64(time.Second) / *rate)
	var wg sync.WaitGroup
	stop := time.Now().Add(*duration)
	for i := 0; i < *senders; i++ {
		w := writers[i]
		if w == nil {
			continue
		}
		wg.Add(1)
		go func(i int, w io.Writer) {
			defer wg.Done()
			time.Sleep(time.Duration(i) * interval / time.Duration(*senders))
			for n := 0; time.Now().Before(stop); n++ {
				eol := "\n"
				if *usePty {
					eol = "\r"
				}
				fmt.Fprintf(w, "BENCH:%d s%d-%d%s", time.Now().UnixNano(), i, n, eol)
				sent.Add(1)
				time.Sleep(interval)
			}
		}(i, w)
	}
	wg.Wait()
	time.Sleep(3 * time.Second)
	close(done)

	mu.Lock()
	defer mu.Unlock()
	sort.Slice(latencies, func(a, b int) bool { return latencies[a] < latencies[b] })
	pct := func(p float64) time.Duration {
		if len(latencies) == 0 {
			return 0
		}
		return latencies[min(len(latencies)-1, int(p*float64(len(latencies))))].Round(100 * time.Microsecond)
	}
	listeners := int64(connected)
	if *usePty {
		listeners = int64(connected - *senders)
	}
	expected := sent.Load() * listeners
	fmt.Printf("sent %d messages, %d deliveries (expected %d, %.1f%%)\n", sent.Load(), received.Load(), expected,
		100*float64(received.Load())/float64(max(1, expected)))
	fmt.Printf("fan-out latency  p50 %s  p95 %s  p99 %s  max %s\n", pct(0.50), pct(0.95), pct(0.99), pct(1))
	if failed.Load() > 0 {
		fmt.Fprintf(os.Stderr, "%d sessions failed to connect\n", failed.Load())
	}
}
