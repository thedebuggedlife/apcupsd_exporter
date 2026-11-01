package apcupsdexporter

import (
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
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mdlayher/apcupsd"
	"github.com/mdlayher/apcupsd_exporter/internal/fakenis"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func TestExporterDescribeDoesNotDial(t *testing.T) {
	var dials int32
	e := New(func(context.Context) (*apcupsd.Client, error) {
		atomic.AddInt32(&dials, 1)
		return nil, errors.New("Describe must not dial")
	})

	got := describedNames(t, e)

	want := []string{
		"apcupsd_battery_charge_percent",
		"apcupsd_battery_cumulative_time_on_seconds_total",
		"apcupsd_battery_date_time_seconds",
		"apcupsd_battery_nominal_volts",
		"apcupsd_battery_number_transfers_total",
		"apcupsd_battery_time_left_seconds",
		"apcupsd_battery_time_on_seconds",
		"apcupsd_battery_volts",
		"apcupsd_exporter_build_info",
		"apcupsd_info",
		"apcupsd_internal_temperature_celsius",
		"apcupsd_last_selftest_time_seconds",
		"apcupsd_last_transfer_off_battery_time_seconds",
		"apcupsd_last_transfer_on_battery_reason",
		"apcupsd_last_transfer_on_battery_time_seconds",
		"apcupsd_line_frequency_hertz",
		"apcupsd_line_nominal_volts",
		"apcupsd_line_volts",
		"apcupsd_nominal_power_watts",
		"apcupsd_output_amps",
		"apcupsd_output_volts",
		"apcupsd_status_low_battery",
		"apcupsd_status_on_battery",
		"apcupsd_status_online",
		"apcupsd_up",
		"apcupsd_ups_load_percent",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("unexpected descriptors:\ngot:  %v\nwant: %v", got, want)
	}

	if n := atomic.LoadInt32(&dials); n != 0 {
		t.Errorf("Describe dialled the NIS %d times", n)
	}
}

func TestExporterDialFailureReportsDown(t *testing.T) {
	var logs bytes.Buffer
	log.SetOutput(&logs)
	defer log.SetOutput(os.Stderr)

	out := testCollector(t, New(func(context.Context) (*apcupsd.Client, error) {
		return nil, errors.New("connection refused")
	}))

	want := []string{`apcupsd_exporter_build_info{version="dev"} 1`, "apcupsd_up 0"}
	if got := apcupsdSeries(out); !slices.Equal(got, want) {
		t.Errorf("unexpected series:\ngot:  %q\nwant: %q", got, want)
	}

	if !strings.Contains(logs.String(), "connection refused") {
		t.Errorf("dial error was not logged: %q", logs.String())
	}
}

func TestExporterBadNISFieldReportsDown(t *testing.T) {
	var logs bytes.Buffer
	log.SetOutput(&logs)
	defer log.SetOutput(os.Stderr)

	srv, err := fakenis.Listen("127.0.0.1:0", []string{"UPSNAME  : example-ups\n", "LINEV    : N/A\n"})
	if err != nil {
		t.Fatalf("failed to start fake NIS: %v", err)
	}
	defer srv.Close()

	out := testCollector(t, New(func(ctx context.Context) (*apcupsd.Client, error) {
		return apcupsd.DialContext(ctx, "tcp", srv.Addr())
	}))

	want := []string{`apcupsd_exporter_build_info{version="dev"} 1`, "apcupsd_up 0"}
	if got := apcupsdSeries(out); !slices.Equal(got, want) {
		t.Errorf("unexpected series:\ngot:  %q\nwant: %q", got, want)
	}

	if !strings.Contains(logs.String(), "failed collecting UPS metrics") {
		t.Errorf("parse error was not logged: %q", logs.String())
	}
}

func TestExporterIncompleteResponseReportsDown(t *testing.T) {
	full, err := fakenis.Response("full")
	if err != nil {
		t.Fatalf("failed to load response: %v", err)
	}

	tests := []struct {
		desc     string
		messages []string
	}{
		{desc: "closed with no response"},
		{desc: "closed partway through", messages: full[:len(full)/2]},
		{desc: "closed just before END APC", messages: full[:len(full)-1]},
	}

	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			log.SetOutput(io.Discard)
			defer log.SetOutput(os.Stderr)

			addr := closingNIS(t, tt.messages)

			out := testCollector(t, New(func(ctx context.Context) (*apcupsd.Client, error) {
				return apcupsd.DialContext(ctx, "tcp", addr)
			}))

			want := []string{`apcupsd_exporter_build_info{version="dev"} 1`, "apcupsd_up 0"}
			if got := apcupsdSeries(out); !slices.Equal(got, want) {
				t.Errorf("unexpected series:\ngot:  %q\nwant: %q", got, want)
			}
		})
	}
}

func TestExporterTerminatorWithNoMessagesReportsDown(t *testing.T) {
	log.SetOutput(io.Discard)
	defer log.SetOutput(os.Stderr)

	srv, err := fakenis.Listen("127.0.0.1:0", nil)
	if err != nil {
		t.Fatalf("failed to start fake NIS: %v", err)
	}
	defer srv.Close()

	out := testCollector(t, New(func(ctx context.Context) (*apcupsd.Client, error) {
		return apcupsd.DialContext(ctx, "tcp", srv.Addr())
	}))

	want := []string{`apcupsd_exporter_build_info{version="dev"} 1`, "apcupsd_up 0"}
	if got := apcupsdSeries(out); !slices.Equal(got, want) {
		t.Errorf("unexpected series:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestBuildInfoReportsVersion(t *testing.T) {
	old := Version
	Version = "v1.2.3"
	defer func() { Version = old }()

	messages, err := fakenis.Response("full")
	if err != nil {
		t.Fatalf("failed to load response: %v", err)
	}

	srv, err := fakenis.Listen("127.0.0.1:0", messages)
	if err != nil {
		t.Fatalf("failed to start fake NIS: %v", err)
	}
	defer srv.Close()

	out := testCollector(t, New(func(ctx context.Context) (*apcupsd.Client, error) {
		return apcupsd.DialContext(ctx, "tcp", srv.Addr())
	}))

	if want := `apcupsd_exporter_build_info{version="v1.2.3"} 1`; !strings.Contains(string(out), want) {
		t.Errorf("missing %q in output:\n%s", want, out)
	}
}

// The golden files are from the v0.3.0 binary or written by hand, so never regenerate them from the working tree.
func TestOutputBaseline(t *testing.T) {
	for _, name := range []string{"full", "no-outcurnt", "commlost-stale", "commlost"} {
		t.Run(name, func(t *testing.T) {
			messages, err := fakenis.Response(name)
			if err != nil {
				t.Fatalf("failed to load response: %v", err)
			}

			srv, err := fakenis.Listen("127.0.0.1:0", messages)
			if err != nil {
				t.Fatalf("failed to start fake NIS: %v", err)
			}
			defer srv.Close()

			golden := filepath.Join("testdata", "golden", name+".prom")
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("failed to read golden file: %v", err)
			}

			got := testCollector(t, New(func(ctx context.Context) (*apcupsd.Client, error) {
				return apcupsd.DialContext(ctx, "tcp", srv.Addr())
			}))

			if !bytes.Equal(got, want) {
				t.Errorf("output differs from %s\n--- got\n%s\n--- want\n%s", golden, got, want)
			}
		})
	}
}

// describedNames returns the sorted metric names that collector describes.
func describedNames(t *testing.T, collector prometheus.Collector) []string {
	t.Helper()

	ch := make(chan *prometheus.Desc, 64)
	collector.Describe(ch)
	close(ch)

	fqName := regexp.MustCompile(`fqName: "([^"]+)"`)
	var names []string
	for d := range ch {
		m := fqName.FindStringSubmatch(d.String())
		if m == nil {
			t.Fatalf("no fqName in descriptor: %v", d)
		}
		names = append(names, m[1])
	}
	sort.Strings(names)

	return names
}

// closingNIS sends messages after each command, then closes with no zero-length terminator.
func closingNIS(t *testing.T, messages []string) string {
	t.Helper()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	t.Cleanup(func() { l.Close() })

	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}

			command := make([]byte, 8)
			if _, err := io.ReadFull(c, command); err == nil {
				for _, m := range messages {
					_, _ = c.Write(binary.BigEndian.AppendUint16(nil, uint16(len(m))))
					_, _ = c.Write([]byte(m))
				}
			}
			c.Close()
		}
	}()

	return l.Addr().String()
}

// apcupsdSeries returns the sample lines of the apcupsd families in out.
func apcupsdSeries(out []byte) []string {
	var series []string
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "apcupsd_") {
			series = append(series, line)
		}
	}

	return series
}

// testCollector scrapes collector and fails the test unless the scrape returns 200.
func testCollector(t *testing.T, collector prometheus.Collector) []byte {
	t.Helper()

	reg := prometheus.NewPedanticRegistry()
	reg.MustRegister(collector)

	srv := httptest.NewServer(promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	defer srv.Close()

	c := &http.Client{Timeout: 1 * time.Second}
	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatalf("failed to HTTP GET data from prometheus: %v", err)
	}
	defer resp.Body.Close()

	buf, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read server response: %v", err)
	}

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected HTTP status: got %d, want 200\n%s", resp.StatusCode, buf)
	}

	return buf
}
