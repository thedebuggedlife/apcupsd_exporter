package apcupsdexporter

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mdlayher/apcupsd"
)

var endAPC = time.Unix(100004, 0)

func TestUPSCollector(t *testing.T) {
	tests := []struct {
		desc       string
		ss         *testStatusSource
		matches    []*regexp.Regexp
		notMatches []*regexp.Regexp
	}{
		{
			desc: "empty",
			ss: &testStatusSource{
				s: &apcupsd.Status{EndAPC: endAPC},
			},
			notMatches: []*regexp.Regexp{
				regexp.MustCompile(`apcupsd_status_`),
				regexp.MustCompile(`apcupsd_line_frequency_hertz`),
				regexp.MustCompile(`apcupsd_battery_date_time_seconds`),
				regexp.MustCompile(`apcupsd_last_transfer_on_battery_reason`),
			},
		},
		{
			desc: "full",
			ss: &testStatusSource{
				s: &apcupsd.Status{
					EndAPC:   endAPC,
					Hostname: "foo",
					Model:    "APC UPS",
					UPSName:  "bar",
					Status:   "ONLINE",

					StatusFlags:             "0x05000008",
					LineFrequency:           60.0,
					BatteryDate:             "2023-01-01",
					LastTransfer:            "Input frequency out of range",
					BatteryChargePercent:    100.0,
					CumulativeTimeOnBattery: 30 * time.Second,
					NominalBatteryVoltage:   12.0,
					TimeLeft:                2 * time.Minute,
					TimeOnBattery:           10 * time.Second,
					BatteryVoltage:          13.2,
					NominalInputVoltage:     120.0,
					LineVoltage:             121.1,
					OutputVoltage:           120.9,
					OutputAmps:              1.06,
					LoadPercent:             16.0,
					NumberTransfers:         1,
					XOnBattery:              time.Unix(100001, 0),
					XOffBattery:             time.Unix(100002, 0),
					LastSelftest:            time.Unix(100003, 0),
					NominalPower:            50.0,
					InternalTemp:            26.4,
				},
			},
			matches: []*regexp.Regexp{
				regexp.MustCompile(`apcupsd_battery_charge_percent{ups="bar"} 100`),
				regexp.MustCompile(`apcupsd_battery_cumulative_time_on_seconds_total{ups="bar"} 30`),
				regexp.MustCompile(`apcupsd_battery_nominal_volts{ups="bar"} 12`),
				regexp.MustCompile(`apcupsd_battery_time_left_seconds{ups="bar"} 120`),
				regexp.MustCompile(`apcupsd_battery_time_on_seconds{ups="bar"} 10`),
				regexp.MustCompile(`apcupsd_battery_volts{ups="bar"} 13.2`),
				regexp.MustCompile(`apcupsd_battery_number_transfers_total{ups="bar"} 1`),
				regexp.MustCompile(`apcupsd_info{hostname="foo",model="APC UPS",status="ONLINE",ups="bar"} 1`),
				regexp.MustCompile(`apcupsd_line_nominal_volts{ups="bar"} 120`),
				regexp.MustCompile(`apcupsd_line_volts{ups="bar"} 121.1`),
				regexp.MustCompile(`apcupsd_output_volts{ups="bar"} 120.9`),
				regexp.MustCompile(`apcupsd_output_amps{ups="bar"} 1.06`),
				regexp.MustCompile(`apcupsd_ups_load_percent{ups="bar"} 16`),
				regexp.MustCompile(`apcupsd_last_transfer_on_battery_time_seconds{ups="bar"} 100001`),
				regexp.MustCompile(`apcupsd_last_transfer_off_battery_time_seconds{ups="bar"} 100002`),
				regexp.MustCompile(`apcupsd_last_selftest_time_seconds{ups="bar"} 100003`),
				regexp.MustCompile(`apcupsd_nominal_power_watts{ups="bar"} 50`),
				regexp.MustCompile(`apcupsd_internal_temperature_celsius{ups="bar"} 26.4`),
				regexp.MustCompile(`apcupsd_status_online{ups="bar"} 1`),
				regexp.MustCompile(`apcupsd_status_on_battery{ups="bar"} 0`),
				regexp.MustCompile(`apcupsd_status_low_battery{ups="bar"} 0`),
				regexp.MustCompile(`apcupsd_line_frequency_hertz{ups="bar"} 60`),
				regexp.MustCompile(`apcupsd_battery_date_time_seconds{ups="bar"} 1.6725312e\+09`),
				regexp.MustCompile(`apcupsd_last_transfer_on_battery_reason{reason="Input frequency out of range",ups="bar"} 1`),
				regexp.MustCompile(`(?m)^apcupsd_up 1$`),
			},
		},
		{
			desc: "output amps not reported",
			ss: &testStatusSource{
				s: &apcupsd.Status{
					EndAPC:        endAPC,
					UPSName:       "bar",
					OutputVoltage: 120.9,
				},
			},
			matches: []*regexp.Regexp{
				regexp.MustCompile(`apcupsd_output_volts{ups="bar"} 120.9`),
			},
			notMatches: []*regexp.Regexp{
				regexp.MustCompile(`apcupsd_output_amps`),
			},
		},
		{
			desc: "line frequency of zero",
			ss: &testStatusSource{
				s: &apcupsd.Status{EndAPC: endAPC, UPSName: "bar", LineFrequency: 0},
			},
			notMatches: []*regexp.Regexp{
				regexp.MustCompile(`apcupsd_line_frequency_hertz`),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			out := testCollector(t, NewUPSCollector(tt.ss))

			for _, m := range tt.matches {
				if !m.Match(out) {
					t.Fatalf("output failed to match regex (regexp: %v)", m)
				}
			}

			for _, m := range tt.notMatches {
				if m.Match(out) {
					t.Fatalf("output unexpectedly matched regex (regexp: %v):\n%s", m, out)
				}
			}
		})
	}
}

func TestUPSCollectorStatusGauges(t *testing.T) {
	tests := []struct {
		desc   string
		status string
		flags  string

		omitted                    bool
		online, onBattery, lowBatt int
	}{
		{desc: "flags online", status: "ONLINE", flags: "0x05000008", online: 1},
		{desc: "flags on battery and low", status: "ONBATT LOWBATT", flags: "0x05000050", onBattery: 1, lowBatt: 1},
		{desc: "flags with trailing text", status: "SHUTTING DOWN", flags: "0x07000050 Status Flag", onBattery: 1, lowBatt: 1},
		{desc: "flags win when STATUS is replaced by shutdown", status: "SHUTTING DOWN", flags: "0x05000250", onBattery: 1, lowBatt: 1},
		{desc: "flags without STATUS", flags: "0x05000010", onBattery: 1},

		{desc: "STATUS when flags are missing", status: "ONLINE REPLACEBATT", online: 1},
		{desc: "STATUS on battery and low", status: "ONBATT LOWBATT", onBattery: 1, lowBatt: 1},
		{desc: "STATUS when flags are unreadable", status: "ONBATT", flags: "garbage", onBattery: 1},
		{desc: "STATUS when flags show no power state", status: "ONLINE", flags: "0x05000000", online: 1},

		{desc: "neither is usable", flags: "garbage", omitted: true},
		{desc: "flags show no power state and STATUS is blank", flags: "0x05000000", omitted: true},
		{desc: "flags show no power state during shutdown", status: "SHUTTING DOWN", flags: "0x05000200", omitted: true},
		{desc: "COMMLOST keeps a stale online bit", status: "COMMLOST", flags: "0x05000108", omitted: true},
		{desc: "COMMLOST without flags", status: "COMMLOST", omitted: true},
		{desc: "COMMLOST with no commlost bit", status: "COMMLOST", flags: "0x05000008", omitted: true},
		{desc: "commlost bit behind SHUTTING DOWN", status: "SHUTTING DOWN", flags: "0x05000308", omitted: true},
		{desc: "SHUTTING DOWN without flags", status: "SHUTTING DOWN", omitted: true},
		{desc: "STATUS without a power state", status: "REPLACEBATT", omitted: true},
	}

	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			out := testCollector(t, NewUPSCollector(&testStatusSource{
				s: &apcupsd.Status{EndAPC: endAPC, UPSName: "bar", Status: tt.status, StatusFlags: tt.flags},
			}))

			if tt.omitted {
				if m := regexp.MustCompile(`apcupsd_status_`); m.Match(out) {
					t.Fatalf("status gauges unexpectedly present:\n%s", out)
				}
				return
			}

			for name, want := range map[string]int{
				"online":      tt.online,
				"on_battery":  tt.onBattery,
				"low_battery": tt.lowBatt,
			} {
				line := fmt.Sprintf("apcupsd_status_%s{ups=\"bar\"} %d\n", name, want)
				if !strings.Contains(string(out), line) {
					t.Errorf("missing %q in output:\n%s", line, out)
				}
			}
		})
	}
}

func TestUPSCollectorOmitsReadingsOnLostContact(t *testing.T) {
	tests := []struct {
		desc   string
		status string
		flags  string
	}{
		{desc: "STATUS and flags", status: "COMMLOST", flags: "0x05000108"},
		{desc: "STATUS only", status: "COMMLOST"},
		{desc: "STATUS without the bit", status: "COMMLOST", flags: "0x05000008"},
		{desc: "bit behind SHUTTING DOWN", status: "SHUTTING DOWN", flags: "0x05000308"},
	}

	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			out := testCollector(t, NewUPSCollector(&testStatusSource{
				s: &apcupsd.Status{
					EndAPC:          endAPC,
					UPSName:         "bar",
					Status:          tt.status,
					StatusFlags:     tt.flags,
					LineVoltage:     118,
					OutputAmps:      1.06,
					LineFrequency:   60,
					BatteryDate:     "2023-01-01",
					LastTransfer:    "Low line voltage",
					NumberTransfers: 3,
				},
			}))

			series := apcupsdSeries(out)

			want := []string{
				fmt.Sprintf(`apcupsd_info{hostname="",model="",status=%q,ups="bar"} 1`, tt.status),
				"apcupsd_up 1",
			}
			if !slices.Equal(series, want) {
				t.Fatalf("unexpected series:\ngot:  %q\nwant: %q", series, want)
			}
		})
	}
}

func TestUPSCollectorStatusErrorReportsDown(t *testing.T) {
	out := testCollector(t, NewUPSCollector(&testStatusSource{err: errors.New("parse failed")}))

	want := []string{"apcupsd_up 0"}
	if got := apcupsdSeries(out); !slices.Equal(got, want) {
		t.Fatalf("unexpected series:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestUPSCollectorBatteryDate(t *testing.T) {
	tests := []struct {
		date string
		want string
	}{
		{date: "2023-01-01", want: "1.6725312e+09"},
		{date: "2001-09-21", want: "1.0010304e+09"},

		{date: ""},
		{date: "N/A"},
		{date: "03/05/18"},
		{date: "1980-00-00"},
		{date: "2023-02-31"},
	}

	for _, tt := range tests {
		t.Run(tt.date, func(t *testing.T) {
			out := testCollector(t, NewUPSCollector(&testStatusSource{
				s: &apcupsd.Status{EndAPC: endAPC, UPSName: "bar", BatteryDate: tt.date},
			}))

			if tt.want == "" {
				if strings.Contains(string(out), "apcupsd_battery_date_time_seconds") {
					t.Fatalf("battery date unexpectedly present:\n%s", out)
				}
				return
			}

			line := fmt.Sprintf("apcupsd_battery_date_time_seconds{ups=\"bar\"} %s\n", tt.want)
			if !strings.Contains(string(out), line) {
				t.Errorf("missing %q in output:\n%s", line, out)
			}
		})
	}
}

var _ StatusSource = &testStatusSource{}

type testStatusSource struct {
	s   *apcupsd.Status
	err error
}

func (ss *testStatusSource) Status() (*apcupsd.Status, error) {
	return ss.s, ss.err
}
