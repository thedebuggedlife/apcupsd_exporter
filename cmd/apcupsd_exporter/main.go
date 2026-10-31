// Command apcupsd_exporter provides a Prometheus exporter for apcupsd.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/mdlayher/apcupsd"
	apcupsdexporter "github.com/mdlayher/apcupsd_exporter"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// healthzPath is reserved for the container healthcheck.
const healthzPath = "/healthz"

// healthcheckTimeout must stay below the HEALTHCHECK timeout in the Dockerfile.
const healthcheckTimeout = 3 * time.Second

var (
	telemetryAddr = flag.String("telemetry.addr", ":9162", "address for apcupsd exporter")
	metricsPath   = flag.String("telemetry.path", "/metrics", "URL path for surfacing collected metrics")

	apcupsdAddr    = flag.String("apcupsd.addr", ":3551", "address of apcupsd Network Information Server (NIS)")
	apcupsdNetwork = flag.String("apcupsd.network", "tcp", `network of apcupsd Network Information Server (NIS): typically "tcp", "tcp4", or "tcp6"`)

	metricsSystem = flag.Bool("metrics.system", true, "enable or disable default go, process, and promhttp metrics")

	healthcheck = flag.Bool("healthcheck", false, "probe the exporter on '-telemetry.addr' and exit: 0 if it is serving, 1 if not")
)

func main() {
	flag.Parse()

	if *healthcheck {
		os.Exit(runHealthcheck(*telemetryAddr))
	}

	if *apcupsdAddr == "" {
		log.Fatal("address of apcupsd Network Information Server (NIS) must be specified with '-apcupsd.addr' flag")
	}

	if err := validateMetricsPath(*metricsPath); err != nil {
		log.Fatalf("invalid '-telemetry.path' flag %q: %v", *metricsPath, err)
	}

	if !*metricsSystem {
		log.Println("Disabling default system metrics (go_*, process_*, promhttp_*)")
	}

	h := newHandler(*metricsPath, *metricsSystem, newClient(*apcupsdNetwork, *apcupsdAddr))

	log.Printf("starting apcupsd exporter on %q for server %s://%s",
		*telemetryAddr, *apcupsdNetwork, *apcupsdAddr)

	if err := newServer(*telemetryAddr, h).ListenAndServe(); err != nil {
		log.Fatalf("cannot start apcupsd exporter: %s", err)
	}
}

// validateMetricsPath accepts only plain literal paths that the mux can serve as given.
func validateMetricsPath(p string) error {
	if !strings.HasPrefix(p, "/") {
		return errors.New("must start with /")
	}

	if p == "/" || strings.TrimSuffix(p, "/") == healthzPath {
		return errors.New("path is reserved")
	}

	if strings.IndexFunc(p, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r) || strings.ContainsRune("{}?#%", r)
	}) >= 0 {
		return errors.New("must not contain whitespace, control characters or any of {}?#%")
	}

	clean := path.Clean(p)
	if clean != "/" && strings.HasSuffix(p, "/") {
		clean += "/"
	}
	if p != clean {
		return errors.New("must not contain empty, . or .. segments")
	}

	return nil
}

func newHandler(metricsPath string, system bool, fn apcupsdexporter.ClientFunc) http.Handler {
	reg := prometheus.NewRegistry()
	reg.MustRegister(apcupsdexporter.New(fn))

	metrics := promhttp.HandlerFor(reg, promhttp.HandlerOpts{ErrorLog: log.Default()})
	if system {
		reg.MustRegister(
			collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
			collectors.NewGoCollector(),
		)
		metrics = promhttp.InstrumentMetricHandler(reg, metrics)
	}

	mux := http.NewServeMux()
	mux.Handle(metricsPath, metrics)
	mux.HandleFunc(healthzPath, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, metricsPath, http.StatusMovedPermanently)
	})

	return mux
}

func newServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		// Must outlast the exporter's 5s scrape limit, or a slow scrape gets no response.
		WriteTimeout: 15 * time.Second,
	}
}

// runHealthcheck probes the exporter listening on addr and returns the process exit code.
func runHealthcheck(addr string) int {
	if err := probeHealthz(addr); err != nil {
		log.Printf("healthcheck failed: %v", err)
		return 1
	}
	return 0
}

func probeHealthz(addr string) error {
	u, err := healthcheckURL(addr)
	if err != nil {
		return err
	}

	resp, err := newHealthcheckClient().Get(u)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: unexpected status: %s", u, resp.Status)
	}
	return nil
}

// healthcheckURL derives the probe URL from a listen address.
func healthcheckURL(addr string) (string, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", fmt.Errorf("invalid '-telemetry.addr' flag %q: %w", addr, err)
	}

	portNum, err := net.LookupPort("tcp", port)
	if err != nil {
		return "", fmt.Errorf("invalid '-telemetry.addr' flag %q: %w", addr, err)
	}
	if portNum == 0 {
		return "", fmt.Errorf("invalid '-telemetry.addr' flag %q: a fixed port is required", addr)
	}

	// Loopback is IPv4 because Go's wildcard listeners are normally dual-stack and ::1 may not exist.
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
	}

	u := url.URL{Scheme: "http", Host: net.JoinHostPort(host, strconv.Itoa(portNum)), Path: healthzPath}
	return u.String(), nil
}

func newHealthcheckClient() *http.Client {
	return &http.Client{
		Timeout: healthcheckTimeout,
		// A transport without a Proxy keeps the probe on the exporter's own listener.
		Transport: &http.Transport{},
		// Binaries without /healthz redirect it to the metrics path, which dials apcupsd.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func newClient(network, addr string) apcupsdexporter.ClientFunc {
	return func(ctx context.Context) (*apcupsd.Client, error) {
		var d net.Dialer
		conn, err := d.DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}

		// The deadline is the only way out of a stuck Status: Client.Close waits for it.
		if deadline, ok := ctx.Deadline(); ok {
			if err := conn.SetDeadline(deadline); err != nil {
				conn.Close()
				return nil, err
			}
		}

		return apcupsd.New(conn), nil
	}
}
