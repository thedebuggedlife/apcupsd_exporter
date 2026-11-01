package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mdlayher/apcupsd"
	"github.com/mdlayher/apcupsd_exporter/internal/fakenis"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// runMainEnv makes the test binary run main, so a test can exercise real flag handling.
const runMainEnv = "APCUPSD_EXPORTER_TEST_RUN_MAIN"

func TestMain(m *testing.M) {
	if os.Getenv(runMainEnv) != "" {
		main()
		return
	}
	os.Exit(m.Run())
}

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

	var b bytes.Buffer
	for _, msg := range []string{"UPSNAME  : ups1\n", "END APC  : 2024-01-02 03:04:06 -0500\n"} {
		_ = binary.Write(&b, binary.BigEndian, uint16(len(msg)))
		b.WriteString(msg)
	}
	_ = binary.Write(&b, binary.BigEndian, uint16(0))
	_, _ = c.Write(b.Bytes())
}

// cannedNIS serves the named fake NIS response and returns its address.
func cannedNIS(t *testing.T, name string) string {
	t.Helper()

	messages, err := fakenis.Response(name)
	if err != nil {
		t.Fatalf("failed to load response: %v", err)
	}

	srv, err := fakenis.Listen("127.0.0.1:0", messages)
	if err != nil {
		t.Fatalf("failed to start fake NIS: %v", err)
	}
	t.Cleanup(func() { srv.Close() })

	return srv.Addr()
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

func TestScrapeReportsDownWhenNISNeverReplies(t *testing.T) {
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

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected status: %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read response: %v", err)
	}
	if !strings.Contains(string(body), "\napcupsd_up 0\n") {
		t.Fatalf("response does not report apcupsd as down:\n%s", body)
	}
	if !strings.Contains(string(body), "\ngo_goroutines ") {
		t.Fatalf("response lost the system metrics:\n%s", body)
	}
	if got := accepted(); got <= dialsBefore {
		t.Fatalf("scrape never reached the NIS: %d dials before and after", got)
	}
	if elapsed := time.Since(start); elapsed < 4*time.Second {
		t.Fatalf("scrape ended after %v, before the timeout could have fired", elapsed)
	}

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

func TestUnreachableNISIsLogged(t *testing.T) {
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

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	// The response doesn't carry the error, so the log is the only trace of it.
	if got := logs.String(); !strings.Contains(got, "error creating apcupsd client") {
		t.Fatalf("dial error was not logged, logs: %q", got)
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

var metricsPathCases = []struct {
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

func TestValidateMetricsPath(t *testing.T) {
	for _, tt := range metricsPathCases {
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

func TestAcceptedMetricsPathsServeOnRealMux(t *testing.T) {
	addr := respondingNIS(t)

	for _, tt := range metricsPathCases {
		if !tt.ok {
			continue
		}

		t.Run(tt.path, func(t *testing.T) {
			srv := httptest.NewServer(newHandler(tt.path, false, newClient("tcp", addr)))
			defer srv.Close()

			client := &http.Client{Timeout: 5 * time.Second}
			for _, target := range []string{tt.path, strings.TrimSuffix(tt.path, "/"), "/", "/unknown"} {
				// The client gives up on a redirect loop, so err covers that failure.
				resp, err := client.Get(srv.URL + (&url.URL{Path: target}).EscapedPath())
				if err != nil {
					t.Fatalf("GET %q failed: %v", target, err)
				}
				resp.Body.Close()

				if resp.StatusCode != http.StatusOK {
					t.Errorf("GET %q: unexpected status: %d", target, resp.StatusCode)
				}
				if got := resp.Request.URL.Path; got != tt.path {
					t.Errorf("GET %q ended on %q, want %q", target, got, tt.path)
				}
			}
		})
	}
}

func TestOversizedNISMessageFailsOnlyThatScrape(t *testing.T) {
	oversized := cannedNIS(t, "oversized")
	full := cannedNIS(t, "full")

	var nisAddr atomic.Value
	nisAddr.Store(oversized)
	fn := func(ctx context.Context) (*apcupsd.Client, error) {
		return newClient("tcp", nisAddr.Load().(string))(ctx)
	}

	var logs lockedBuffer
	log.SetOutput(&logs)
	defer log.SetOutput(os.Stderr)

	srv := httptest.NewServer(newHandler("/metrics", true, fn))
	defer srv.Close()

	client := &http.Client{Timeout: 10 * time.Second}
	scrape := func() (int, string) {
		t.Helper()

		resp, err := client.Get(srv.URL + "/metrics")
		if err != nil {
			t.Fatalf("scrape did not complete: %v", err)
		}
		defer resp.Body.Close()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("failed to read response: %v", err)
		}
		return resp.StatusCode, string(body)
	}

	if code, body := scrape(); code != http.StatusInternalServerError {
		t.Fatalf("unexpected status from oversized message: %d\n%s", code, body)
	}

	got := logs.String()
	for _, want := range []string{"error gathering metrics", "collector panic recovered", "slice bounds out of range"} {
		if !strings.Contains(got, want) {
			t.Errorf("panic was not logged by the handler, want %q in logs: %q", want, got)
		}
	}

	nisAddr.Store(full)

	code, body := scrape()
	if code != http.StatusOK {
		t.Fatalf("unexpected status after recovery: %d\n%s", code, body)
	}
	if !strings.Contains(body, `ups="example-ups"`) {
		t.Errorf("scrape after recovery has no UPS metrics:\n%s", body)
	}
}

// serverAddr returns the host:port of an httptest server.
func serverAddr(srv *httptest.Server) string {
	return srv.Listener.Addr().String()
}

// closedPort returns an address that nothing is listening on.
func closedPort(t *testing.T) string {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	defer ln.Close()

	return ln.Addr().String()
}

func TestHealthcheckURL(t *testing.T) {
	tests := []struct {
		addr string
		want string
	}{
		{addr: ":9162", want: "http://127.0.0.1:9162/healthz"},
		{addr: "0.0.0.0:9162", want: "http://127.0.0.1:9162/healthz"},
		{addr: "[::]:9162", want: "http://127.0.0.1:9162/healthz"},
		{addr: "[::1]:9162", want: "http://[::1]:9162/healthz"},
		{addr: "localhost:9162", want: "http://localhost:9162/healthz"},
		{addr: "127.0.0.2:9162", want: "http://127.0.0.2:9162/healthz"},
		{addr: "192.0.2.10:9162", want: "http://192.0.2.10:9162/healthz"},
		{addr: "exporter.example:9162", want: "http://exporter.example:9162/healthz"},
		{addr: "[fe80::1%en0]:9162", want: "http://[fe80::1%25en0]:9162/healthz"},
		{addr: ":http", want: "http://127.0.0.1:80/healthz"},

		{addr: ""},
		{addr: "9162"},
		{addr: "localhost"},
		{addr: "::1"},
		{addr: "[::1"},
		{addr: ":"},
		{addr: "localhost:"},
		{addr: ":0"},
		{addr: ":99999"},
		{addr: ":no-such-port"},
	}

	for _, tt := range tests {
		t.Run(tt.addr, func(t *testing.T) {
			got, err := healthcheckURL(tt.addr)
			if tt.want == "" {
				if err == nil {
					t.Fatalf("expected %q to be rejected, got %q", tt.addr, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("expected %q to be accepted: %v", tt.addr, err)
			}
			if got != tt.want {
				t.Fatalf("unexpected URL:\n- want: %s\n-  got: %s", tt.want, got)
			}
		})
	}
}

func TestRunHealthcheckStatusCodes(t *testing.T) {
	tests := []struct {
		name   string
		status int
		want   int
	}{
		{name: "ok", status: http.StatusOK, want: 0},
		{name: "no content", status: http.StatusNoContent, want: 1},
		{name: "not found", status: http.StatusNotFound, want: 1},
		{name: "server error", status: http.StatusInternalServerError, want: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var method, target atomic.Value
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				method.Store(r.Method)
				target.Store(r.URL.Path)
				w.WriteHeader(tt.status)
			}))
			defer srv.Close()

			if got := runHealthcheck(serverAddr(srv)); got != tt.want {
				t.Fatalf("unexpected exit code: %d", got)
			}
			if method.Load() != http.MethodGet || target.Load() != "/healthz" {
				t.Fatalf("unexpected request: %v %v", method.Load(), target.Load())
			}
		})
	}
}

func TestRunHealthcheckFailsOnClosedPort(t *testing.T) {
	if got := runHealthcheck(closedPort(t)); got != 1 {
		t.Fatalf("unexpected exit code: %d", got)
	}
}

func TestRunHealthcheckReportsMalformedAddress(t *testing.T) {
	var logs lockedBuffer
	log.SetOutput(&logs)
	defer log.SetOutput(os.Stderr)

	if got := runHealthcheck("not-an-address"); got != 1 {
		t.Fatalf("unexpected exit code: %d", got)
	}
	if got := logs.String(); !strings.Contains(got, "not-an-address") {
		t.Fatalf("log does not name the bad address: %q", got)
	}
}

func TestRunHealthcheckDoesNotFollowRedirects(t *testing.T) {
	var followed atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		followed.Add(1)
	}))
	defer target.Close()

	srv := httptest.NewServer(http.RedirectHandler(target.URL+"/metrics", http.StatusMovedPermanently))
	defer srv.Close()

	if got := runHealthcheck(serverAddr(srv)); got != 1 {
		t.Errorf("unexpected exit code: %d", got)
	}
	if got := followed.Load(); got != 0 {
		t.Fatalf("redirect target received %d requests", got)
	}
}

func TestRunHealthcheckTimesOutOnSilentServer(t *testing.T) {
	addr, accepted := silentNIS(t)

	start := time.Now()
	got := runHealthcheck(addr)
	elapsed := time.Since(start)

	if got != 1 {
		t.Fatalf("unexpected exit code: %d", got)
	}
	if accepted() == 0 {
		t.Fatal("probe never connected, so the timeout was not what ended it")
	}
	if elapsed < 2900*time.Millisecond {
		t.Fatalf("probe gave up early, after %v", elapsed)
	}
	// Docker kills the probe at the Dockerfile's 5s timeout, which loses the exit code.
	if elapsed > 4*time.Second {
		t.Fatalf("probe outlived its 3s timeout: %v", elapsed)
	}
}

func TestHealthcheckClientBypassesProxy(t *testing.T) {
	transport, ok := newHealthcheckClient().Transport.(*http.Transport)
	if !ok {
		t.Fatal("expected a dedicated *http.Transport, the default one reads proxy settings")
	}
	if transport.Proxy != nil {
		t.Fatal("probe must stay on the local listener, not go through a proxy")
	}
}

func TestHealthzDoesNotDial(t *testing.T) {
	for _, system := range []bool{true, false} {
		t.Run(fmt.Sprintf("system=%t", system), func(t *testing.T) {
			var dials atomic.Int32
			fn := func(context.Context) (*apcupsd.Client, error) {
				dials.Add(1)
				return nil, errors.New("unreachable")
			}
			h := newHandler("/metrics", system, fn)

			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

			if rec.Code != http.StatusOK {
				t.Fatalf("unexpected status: %d", rec.Code)
			}
			if got := dials.Load(); got != 0 {
				t.Fatalf("/healthz dialled apcupsd %d times", got)
			}

			rec = httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))

			if rec.Code != http.StatusOK {
				t.Fatalf("unexpected /metrics status: %d", rec.Code)
			}
			if !strings.Contains(rec.Body.String(), "\napcupsd_up 0\n") {
				t.Fatalf("/metrics does not report apcupsd as down:\n%s", rec.Body)
			}
			if got := dials.Load(); got != 1 {
				t.Fatalf("expected the scrape to dial once, got %d", got)
			}
		})
	}
}

func TestHealthzDoesNotAddMetrics(t *testing.T) {
	addr := respondingNIS(t)
	h := newHandler("/metrics", true, newClient("tcp", addr))

	before := scrapeFamilies(t, h)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}

	if after := scrapeFamilies(t, h); !reflect.DeepEqual(before, after) {
		t.Fatalf("/healthz changed the metric families:\n- before: %v\n-  after: %v", before, after)
	}
}

func TestRunHealthcheckAgainstRealServerIgnoresDeadNIS(t *testing.T) {
	nisAddr, accepted := silentNIS(t)

	srv := httptest.NewServer(newHandler("/metrics", true, newClient("tcp", nisAddr)))
	defer srv.Close()

	if got := runHealthcheck(serverAddr(srv)); got != 0 {
		t.Fatalf("unexpected exit code: %d", got)
	}
	if got := accepted(); got != 0 {
		t.Fatalf("healthcheck reached apcupsd %d times", got)
	}
}

func TestHealthcheckFlagRunsBeforeStartupChecks(t *testing.T) {
	nisAddr, accepted := silentNIS(t)

	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()

	tests := []struct {
		name string
		addr string
		args []string
		want int
	}{
		{name: "apcupsd address set", addr: serverAddr(srv), args: []string{"-apcupsd.addr=" + nisAddr}, want: 0},
		{name: "apcupsd address empty", addr: serverAddr(srv), args: []string{"-apcupsd.addr="}, want: 0},
		{name: "invalid metrics path", addr: serverAddr(srv), args: []string{"-telemetry.path=/healthz"}, want: 0},
		{name: "closed port", addr: closedPort(t), args: []string{"-apcupsd.addr=" + nisAddr}, want: 1},
		{name: "malformed address", addr: "not-an-address", args: []string{"-apcupsd.addr=" + nisAddr}, want: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], append([]string{"-healthcheck", "-telemetry.addr=" + tt.addr}, tt.args...)...)
			cmd.Env = append(os.Environ(), runMainEnv+"=1")

			out, err := cmd.CombinedOutput()
			var exitErr *exec.ExitError
			if err != nil && !errors.As(err, &exitErr) {
				t.Fatalf("failed to run binary: %v", err)
			}
			if got := cmd.ProcessState.ExitCode(); got != tt.want {
				t.Fatalf("unexpected exit code: %d, output: %s", got, out)
			}
			if got := accepted(); got != 0 {
				t.Fatalf("healthcheck reached apcupsd %d times", got)
			}
		})
	}
}
