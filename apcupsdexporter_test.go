package apcupsdexporter

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
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

	ch := make(chan *prometheus.Desc, 64)
	e.Describe(ch)
	close(ch)

	fqName := regexp.MustCompile(`fqName: "([^"]+)"`)
	var got []string
	for d := range ch {
		m := fqName.FindStringSubmatch(d.String())
		if m == nil {
			t.Fatalf("no fqName in descriptor: %v", d)
		}
		got = append(got, m[1])
	}
	sort.Strings(got)

	want := []string{
		"apcupsd_battery_charge_percent",
		"apcupsd_battery_cumulative_time_on_seconds_total",
		"apcupsd_battery_nominal_volts",
		"apcupsd_battery_number_transfers_total",
		"apcupsd_battery_time_left_seconds",
		"apcupsd_battery_time_on_seconds",
		"apcupsd_battery_volts",
		"apcupsd_info",
		"apcupsd_internal_temperature_celsius",
		"apcupsd_last_selftest_time_seconds",
		"apcupsd_last_transfer_off_battery_time_seconds",
		"apcupsd_last_transfer_on_battery_time_seconds",
		"apcupsd_line_nominal_volts",
		"apcupsd_line_volts",
		"apcupsd_nominal_power_watts",
		"apcupsd_output_amps",
		"apcupsd_output_volts",
		"apcupsd_ups_load_percent",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("unexpected descriptors:\ngot:  %v\nwant: %v", got, want)
	}

	if n := atomic.LoadInt32(&dials); n != 0 {
		t.Errorf("Describe dialled the NIS %d times", n)
	}
}

func TestExporterDialFailureFailsScrape(t *testing.T) {
	e := New(func(context.Context) (*apcupsd.Client, error) {
		return nil, errors.New("connection refused")
	})

	out := testCollectorStatus(t, e, http.StatusInternalServerError)

	if !strings.Contains(string(out), "connection refused") {
		t.Errorf("response does not report the dial error:\n%s", out)
	}
}

// The golden files come from the v0.3.0 binary, so never regenerate them from the working tree.
func TestOutputBaseline(t *testing.T) {
	for _, name := range []string{"full", "no-outcurnt"} {
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

// testCollector scrapes collector and fails the test unless the scrape returns 200.
func testCollector(t *testing.T, collector prometheus.Collector) []byte {
	t.Helper()

	return testCollectorStatus(t, collector, http.StatusOK)
}

// testCollectorStatus is testCollector for tests that expect another status.
func testCollectorStatus(t *testing.T, collector prometheus.Collector, want int) []byte {
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

	if resp.StatusCode != want {
		t.Fatalf("unexpected HTTP status: got %d, want %d\n%s", resp.StatusCode, want, buf)
	}

	return buf
}
