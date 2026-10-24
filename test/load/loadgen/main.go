// Command loadgen puts SFTP load on an SFTPgate server and reports what
// happened: latencies (p50/p95/p99), throughput, errors, and whether every
// file arrived intact.
//
// Each session logs in, then keeps uploading files (and optionally
// downloading them back to compare checksums) until it has done -files
// uploads or -duration has passed. A session whose connection breaks logs
// in again and carries on, so failures are measured, not hidden.
//
// At the end, every upload that reported an error is checked: the file must
// either be missing or be complete and correct. A half file would be a bug.
package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

type options struct {
	addr     string
	user     string
	keyFile  string
	folder   string
	sessions int
	files    int
	duration time.Duration
	ramp     time.Duration
	size     int64
	download bool
	cleanup  bool
	scenario string

	// printSums prints the checksum of every file sent, for checking what
	// the storage kept.
	printSums bool
}

func main() {
	var o options
	flag.StringVar(&o.addr, "addr", "localhost:2222", "SFTPgate address")
	flag.StringVar(&o.user, "user", "loadtest", "account name")
	flag.StringVar(&o.keyFile, "key", "", "private key file")
	flag.StringVar(&o.folder, "folder", "/data", "folder to upload into")
	flag.IntVar(&o.sessions, "sessions", 10, "concurrent SFTP sessions")
	flag.IntVar(&o.files, "files", 0, "uploads per session (0 = until -duration ends)")
	flag.DurationVar(&o.duration, "duration", time.Minute, "how long to run when -files is 0")
	flag.DurationVar(&o.ramp, "ramp", 10*time.Second, "time over which sessions start")
	size := flag.String("size", "100KiB", "file size, like 64KiB, 10MiB")
	flag.BoolVar(&o.download, "download", true, "download each file back and compare checksums")
	flag.BoolVar(&o.cleanup, "cleanup", true, "delete each file after it is checked, to keep storage small")
	flag.StringVar(&o.scenario, "scenario", "load", "name printed in the report")
	flag.BoolVar(&o.printSums, "print-sums", false, "print the checksum of every file sent")
	flag.Parse()

	var err error
	if o.size, err = parseSize(*size); err != nil {
		fail(err)
	}
	signer, err := readKey(o.keyFile)
	if err != nil {
		fail(err)
	}

	report := run(o, signer)
	out, _ := json.Marshal(report)
	// One marker line, so the report can be picked out of the Job's log.
	fmt.Println("LOADGEN-REPORT " + string(out))
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "loadgen:", err)
	os.Exit(1)
}

// --- running ---------------------------------------------------------------

type uploadRecord struct {
	path string
	sha  [32]byte
	size int64
}

type collector struct {
	mu        sync.Mutex
	latencies map[string][]time.Duration
	errors    map[string]map[string]int
	failed    []uploadRecord

	bytesUp, bytesDown atomic.Int64
	mismatches         atomic.Int64
	verified           atomic.Int64
}

func (c *collector) ok(op string, d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.latencies[op] = append(c.latencies[op], d)
}

func (c *collector) err(op string, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.errors[op] == nil {
		c.errors[op] = map[string]int{}
	}
	c.errors[op][shortError(err)]++
}

func run(o options, signer ssh.Signer) Report {
	c := &collector{latencies: map[string][]time.Duration{}, errors: map[string]map[string]int{}}
	runID := fmt.Sprintf("%s-%d", o.scenario, time.Now().UnixNano())
	deadline := time.Now().Add(o.ramp + o.duration)

	start := time.Now()
	var wg sync.WaitGroup
	for i := range o.sessions {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Spread the logins over the ramp time.
			if o.sessions > 1 {
				time.Sleep(o.ramp * time.Duration(i) / time.Duration(o.sessions))
			}
			session(o, signer, c, runID, i, deadline)
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)

	partial, committed := checkFailedUploads(o, signer, c)
	return buildReport(o, c, elapsed, partial, committed)
}

// session runs one SFTP session, logging in again whenever it breaks.
func session(o options, signer ssh.Signer, c *collector, runID string, id int, deadline time.Time) {
	payload := make([]byte, o.size)
	_, _ = rand.Read(payload)

	var client *sftp.Client
	var conn *ssh.Client
	defer func() {
		if client != nil {
			_ = client.Close()
			_ = conn.Close()
		}
	}()

	for n := 0; ; n++ {
		if (o.files > 0 && n >= o.files) || (o.files == 0 && time.Now().After(deadline)) {
			return
		}
		if client == nil {
			var err error
			conn, client, err = login(o, signer, c)
			if err != nil {
				// Back off a little so a dead server isn't hammered.
				time.Sleep(500 * time.Millisecond)
				continue
			}
		}

		// Make each file unique, so every checksum is different.
		binary.BigEndian.PutUint64(payload, uint64(id)<<32|uint64(n))
		rec := uploadRecord{
			path: path.Join(o.folder, fmt.Sprintf("%s-s%04d-f%06d.bin", runID, id, n)),
			sha:  sha256.Sum256(payload),
			size: o.size,
		}

		if o.printSums {
			fmt.Printf("SENT %s %x\n", rec.path, rec.sha)
		}
		if err := upload(client, rec.path, payload, c); err != nil {
			c.mu.Lock()
			c.failed = append(c.failed, rec)
			c.mu.Unlock()
			if broken(err) {
				_ = client.Close()
				_ = conn.Close()
				client = nil
			}
			continue
		}

		if o.download {
			if err := verify(client, rec, c); err != nil && broken(err) {
				_ = client.Close()
				_ = conn.Close()
				client = nil
				continue
			}
		}
		if n%20 == 0 {
			t := time.Now()
			if _, err := client.ReadDir(o.folder); err != nil {
				c.err("list", err)
			} else {
				c.ok("list", time.Since(t))
			}
		}
		if o.cleanup {
			_ = client.Remove(rec.path)
		}
	}
}

func login(o options, signer ssh.Signer, c *collector) (*ssh.Client, *sftp.Client, error) {
	t := time.Now()
	conn, err := ssh.Dial("tcp", o.addr, &ssh.ClientConfig{
		User:            o.user,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         20 * time.Second,
	})
	if err != nil {
		c.err("login", err)
		return nil, nil, err
	}
	client, err := sftp.NewClient(conn, sftp.UseConcurrentWrites(true))
	if err != nil {
		_ = conn.Close()
		c.err("login", err)
		return nil, nil, err
	}
	c.ok("login", time.Since(t))
	return conn, client, nil
}

func upload(client *sftp.Client, p string, data []byte, c *collector) error {
	t := time.Now()
	f, err := client.Create(p)
	if err == nil {
		_, err = f.ReadFrom(bytes.NewReader(data))
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}
	if err != nil {
		c.err("upload", err)
		return err
	}
	c.ok("upload", time.Since(t))
	c.bytesUp.Add(int64(len(data)))
	return nil
}

// verify downloads a file and compares its checksum with what was sent.
func verify(client *sftp.Client, rec uploadRecord, c *collector) error {
	t := time.Now()
	f, err := client.Open(rec.path)
	if err != nil {
		c.err("download", err)
		return err
	}
	h := sha256.New()
	n, err := f.WriteTo(h)
	_ = f.Close()
	if err != nil {
		c.err("download", err)
		return err
	}
	c.ok("download", time.Since(t))
	c.bytesDown.Add(n)
	c.verified.Add(1)
	if !bytes.Equal(h.Sum(nil), rec.sha[:]) {
		c.mismatches.Add(1)
	}
	return nil
}

// checkFailedUploads looks at every upload that reported an error. The file
// must be missing, or (if it was saved just before the connection broke)
// complete and correct. Anything else is a half file.
func checkFailedUploads(o options, signer ssh.Signer, c *collector) (partial, committed int) {
	if len(c.failed) == 0 {
		return 0, 0
	}
	probe := &collector{latencies: map[string][]time.Duration{}, errors: map[string]map[string]int{}}
	conn, client, err := login(o, signer, probe)
	if err != nil {
		fmt.Fprintln(os.Stderr, "loadgen: cannot check failed uploads:", err)
		return -1, -1
	}
	defer conn.Close()
	defer client.Close()

	for _, rec := range c.failed {
		f, err := client.Open(rec.path)
		if err != nil {
			// Missing: the upload was dropped, which is what should happen.
			continue
		}
		h := sha256.New()
		n, _ := f.WriteTo(h)
		_ = f.Close()
		if n == rec.size && bytes.Equal(h.Sum(nil), rec.sha[:]) {
			committed++
		} else {
			partial++
		}
		if o.cleanup {
			_ = client.Remove(rec.path)
		}
	}
	return partial, committed
}

// broken reports whether an error means the connection is gone.
func broken(err error) bool {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, sftp.ErrSSHFxConnectionLost) {
		return true
	}
	s := err.Error()
	return strings.Contains(s, "connection") || strings.Contains(s, "EOF") || strings.Contains(s, "closed")
}

func shortError(err error) string {
	s := err.Error()
	if len(s) > 90 {
		s = s[:90]
	}
	return s
}

// --- report ----------------------------------------------------------------

// Report is the JSON printed at the end.
type Report struct {
	Scenario       string                    `json:"scenario"`
	Sessions       int                       `json:"sessions"`
	FileBytes      int64                     `json:"file_bytes"`
	ElapsedSeconds float64                   `json:"elapsed_seconds"`
	Ops            map[string]OpStats        `json:"ops"`
	Errors         map[string]map[string]int `json:"errors"`
	UploadMBps     float64                   `json:"upload_mb_per_s"`
	DownloadMBps   float64                   `json:"download_mb_per_s"`
	Verified       int64                     `json:"verified_downloads"`
	Mismatches     int64                     `json:"checksum_mismatches"`
	FailedUploads  int                       `json:"failed_uploads"`
	PartialFiles   int                       `json:"partial_files_found"`
	SavedDespite   int                       `json:"failed_but_saved_complete"`
}

// OpStats are the numbers for one kind of operation.
type OpStats struct {
	Count  int     `json:"count"`
	Errors int     `json:"errors"`
	PerSec float64 `json:"per_second"`
	P50ms  float64 `json:"p50_ms"`
	P95ms  float64 `json:"p95_ms"`
	P99ms  float64 `json:"p99_ms"`
	MaxMs  float64 `json:"max_ms"`
}

func buildReport(o options, c *collector, elapsed time.Duration, partial, committed int) Report {
	r := Report{
		Scenario:       o.scenario,
		Sessions:       o.sessions,
		FileBytes:      o.size,
		ElapsedSeconds: round(elapsed.Seconds()),
		Ops:            map[string]OpStats{},
		Errors:         c.errors,
		UploadMBps:     round(float64(c.bytesUp.Load()) / 1e6 / elapsed.Seconds()),
		DownloadMBps:   round(float64(c.bytesDown.Load()) / 1e6 / elapsed.Seconds()),
		Verified:       c.verified.Load(),
		Mismatches:     c.mismatches.Load(),
		FailedUploads:  len(c.failed),
		PartialFiles:   partial,
		SavedDespite:   committed,
	}
	for _, op := range []string{"login", "upload", "download", "list"} {
		lat := c.latencies[op]
		errCount := 0
		for _, n := range c.errors[op] {
			errCount += n
		}
		if len(lat) == 0 && errCount == 0 {
			continue
		}
		sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
		r.Ops[op] = OpStats{
			Count:  len(lat),
			Errors: errCount,
			PerSec: round(float64(len(lat)) / elapsed.Seconds()),
			P50ms:  pct(lat, 0.50),
			P95ms:  pct(lat, 0.95),
			P99ms:  pct(lat, 0.99),
			MaxMs:  pct(lat, 1.0),
		}
	}
	return r
}

// pct returns the p-th percentile in milliseconds (nearest rank).
func pct(sorted []time.Duration, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	i := int(math.Ceil(p*float64(len(sorted)))) - 1
	i = max(0, min(i, len(sorted)-1))
	return round(float64(sorted[i].Microseconds()) / 1000)
}

func round(f float64) float64 {
	return math.Round(f*100) / 100
}

func parseSize(s string) (int64, error) {
	mult := int64(1)
	for suffix, m := range map[string]int64{"KiB": 1 << 10, "MiB": 1 << 20, "GiB": 1 << 30} {
		if strings.HasSuffix(s, suffix) {
			s, mult = strings.TrimSuffix(s, suffix), m
		}
	}
	var n int64
	if _, err := fmt.Sscan(s, &n); err != nil || n <= 0 {
		return 0, fmt.Errorf("bad size %q", s)
	}
	return n * mult, nil
}

func readKey(p string) (ssh.Signer, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	return ssh.ParsePrivateKey(b)
}
