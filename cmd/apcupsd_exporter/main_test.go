package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// silentNIS accepts connections and never replies, holding them open until the
// test ends. It returns the address and a count of accepted connections.
func silentNIS(t *testing.T) (string, func() int32) {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}

	var accepted int32
	var mu sync.Mutex
	var conns []net.Conn

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			atomic.AddInt32(&accepted, 1)
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
		}
	}()

	t.Cleanup(func() {
		ln.Close()
		<-done
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			c.Close()
		}
	})

	return ln.Addr().String(), func() int32 { return atomic.LoadInt32(&accepted) }
}

// respondingNIS answers each status request with a single key/value pair.
func respondingNIS(t *testing.T) string {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go serveStatus(c)
		}
	}()

	return ln.Addr().String()
}

func serveStatus(c net.Conn) {
	defer c.Close()

	var length uint16
	if err := binary.Read(c, binary.BigEndian, &length); err != nil {
		return
	}
	if _, err := io.CopyN(io.Discard, c, int64(length)); err != nil {
		return
	}

	msg := []byte("UPSNAME  : ups1\n")
	var b bytes.Buffer
	_ = binary.Write(&b, binary.BigEndian, uint16(len(msg)))
	b.Write(msg)
	_ = binary.Write(&b, binary.BigEndian, uint16(0))
	_, _ = c.Write(b.Bytes())
}

// scrapeFamilies returns the sorted metric family names from a 200 response.
func scrapeFamilies(t *testing.T, h http.Handler) []string {
	t.Helper()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d, body: %s", rec.Code, rec.Body.String())
	}

	var families []string
	s := bufio.NewScanner(rec.Body)
	for s.Scan() {
		if f := strings.Fields(s.Text()); len(f) == 4 && f[0] == "#" && f[1] == "TYPE" {
			families = append(families, f[2])
		}
	}
	if err := s.Err(); err != nil {
		t.Fatalf("failed to read response: %v", err)
	}

	sort.Strings(families)
	return families
}

func contains(families []string, name string) bool {
	for _, f := range families {
		if f == name {
			return true
		}
	}
	return false
}

func TestNewClientAppliesContextDeadlineToStatus(t *testing.T) {
	addr, _ := silentNIS(t)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	c, err := newClient("tcp", addr)(ctx)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	errC := make(chan error, 1)
	go func() {
		_, err := c.Status()
		errC <- err
	}()

	select {
	case err := <-errC:
		c.Close()
		var nerr net.Error
		if !errors.As(err, &nerr) || !nerr.Timeout() {
			t.Fatalf("expected a timeout error, got: %v", err)
		}
	// Close would block here: the client holds its lock while Status reads.
	case <-time.After(3 * time.Second):
		t.Fatal("Status did not return after the context deadline passed")
	}
}

func TestScrapeFailsWhenNISNeverReplies(t *testing.T) {
	nisAddr, accepted := silentNIS(t)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}

	srv := newServer(ln.Addr().String(), newHandler("/metrics", true, newClient("tcp", nisAddr)))
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	// Registration may dial the NIS, so count only the scrape's own dial.
	dialsBefore := accepted()
	goroutinesBefore := runtime.NumGoroutine()

	client := &http.Client{Timeout: 10 * time.Second}
	start := time.Now()
	resp, err := client.Get("http://" + ln.Addr().String() + "/metrics")
	if err != nil {
		t.Fatalf("scrape did not complete: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("unexpected status: %d", resp.StatusCode)
	}
	if got := accepted(); got <= dialsBefore {
		t.Fatalf("scrape never reached the NIS: %d dials before and after", got)
	}
	if elapsed := time.Since(start); elapsed < 4*time.Second {
		t.Fatalf("scrape failed after %v, before the timeout could have fired", elapsed)
	}

	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	client.CloseIdleConnections()

	var goroutinesAfter int
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if goroutinesAfter = runtime.NumGoroutine(); goroutinesAfter <= goroutinesBefore {
			return
		}
	}

	buf := make([]byte, 1<<16)
	buf = buf[:runtime.Stack(buf, true)]
	t.Fatalf("goroutines left behind: %d before, %d after\n%s", goroutinesBefore, goroutinesAfter, buf)
}

func TestSystemMetricsEnabledMatchDefaultRegistry(t *testing.T) {
	addr := respondingNIS(t)

	want := scrapeFamilies(t, promhttp.Handler())
	families := scrapeFamilies(t, newHandler("/metrics", true, newClient("tcp", addr)))

	var got []string
	for _, f := range families {
		if !strings.HasPrefix(f, "apcupsd_") {
			got = append(got, f)
		}
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("unexpected non-apcupsd families:\n- want: %v\n-  got: %v", want, got)
	}

	required := []string{
		"apcupsd_info",
		"go_goroutines",
		"promhttp_metric_handler_requests_in_flight",
		"promhttp_metric_handler_requests_total",
	}
	if runtime.GOOS == "linux" {
		required = append(required, "process_cpu_seconds_total")
	}
	for _, name := range required {
		if !contains(families, name) {
			t.Errorf("expected family %q in %v", name, families)
		}
	}
}

func TestSystemMetricsDisabledLeavesOnlyExporterMetrics(t *testing.T) {
	addr := respondingNIS(t)

	families := scrapeFamilies(t, newHandler("/metrics", false, newClient("tcp", addr)))

	if !contains(families, "apcupsd_info") {
		t.Errorf("expected family apcupsd_info in %v", families)
	}
	for _, f := range families {
		if !strings.HasPrefix(f, "apcupsd_") {
			t.Errorf("unexpected family: %q", f)
		}
	}
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func TestFailedGatherIsLogged(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close()

	var logs lockedBuffer
	log.SetOutput(&logs)
	defer log.SetOutput(os.Stderr)

	rec := httptest.NewRecorder()
	h := newHandler("/metrics", false, newClient("tcp", addr))
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	// The exporter logs dial errors itself, so look for promhttp's own line.
	if got := logs.String(); !strings.Contains(got, "error gathering metrics") {
		t.Fatalf("failed gather was not logged by the handler, logs: %q", got)
	}
}

func TestCatchAllRedirectsToMetricsPath(t *testing.T) {
	addr := respondingNIS(t)

	tests := []struct {
		name        string
		metricsPath string
		target      string
		code        int
		location    string
	}{
		{name: "root", metricsPath: "/metrics", target: "/", code: http.StatusMovedPermanently, location: "/metrics"},
		{name: "unknown path", metricsPath: "/metrics", target: "/unknown", code: http.StatusMovedPermanently, location: "/metrics"},
		{name: "custom path", metricsPath: "/custom", target: "/metrics", code: http.StatusMovedPermanently, location: "/custom"},
		{name: "metrics path is served", metricsPath: "/custom", target: "/custom", code: http.StatusOK},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h := newHandler(tt.metricsPath, false, newClient("tcp", addr))
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.target, nil))

			if rec.Code != tt.code {
				t.Fatalf("unexpected status: %d", rec.Code)
			}
			if got := rec.Header().Get("Location"); got != tt.location {
				t.Fatalf("unexpected location: %q", got)
			}
		})
	}
}

func TestNewServerSetsTimeouts(t *testing.T) {
	srv := newServer(":0", http.NotFoundHandler())

	if srv.ReadHeaderTimeout <= 0 {
		t.Errorf("ReadHeaderTimeout not set: %v", srv.ReadHeaderTimeout)
	}
	if srv.ReadTimeout <= 0 {
		t.Errorf("ReadTimeout not set: %v", srv.ReadTimeout)
	}
	if srv.WriteTimeout <= 0 {
		t.Errorf("WriteTimeout not set: %v", srv.WriteTimeout)
	}
}

func TestValidateMetricsPath(t *testing.T) {
	tests := []struct {
		path string
		ok   bool
	}{
		{path: "/metrics", ok: true},
		{path: "/metrics/", ok: true},
		{path: "/custom/metrics", ok: true},
		{path: "/metrics-v1_2.txt", ok: true},
		{path: "/café", ok: true},
		{path: "/healthz2", ok: true},
		{path: "/healthz/metrics", ok: true},

		{path: ""},
		{path: "metrics"},
		{path: " /metrics"},
		{path: "/"},
		{path: "/healthz"},
		{path: "/healthz/"},
		{path: "/a b"},
		{path: "/a\tb"},
		{path: "/a\nb"},
		{path: "/a b"},
		{path: "/{x}"},
		{path: "/a{"},
		{path: "/a}"},
		{path: "/a\x00b"},

		// These end in a redirect loop on at least one mux version.
		{path: "//"},
		{path: "/a//b"},
		{path: "/a/../b"},
		{path: "/./a"},
		{path: "/metrics//"},
		{path: "/a?b"},
		{path: "/a#b"},
		{path: "/a%20b"},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			err := validateMetricsPath(tt.path)
			if tt.ok && err != nil {
				t.Fatalf("expected %q to be accepted: %v", tt.path, err)
			}
			if !tt.ok && err == nil {
				t.Fatalf("expected %q to be rejected", tt.path)
			}
		})
	}
}
