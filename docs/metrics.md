# Metrics

Every `apcupsd_*` metric the exporter serves, where it comes from, and what
happens when the UPS doesn't report it. `TestMetricsCatalogueMatchesDescribe`
keeps this file and the code in step: a metric with no row here, or a name here
that the exporter doesn't describe, fails the tests.

The NIS field is the key shown by `apcaccess status`. Each value is parsed by
the `github.com/mdlayher/apcupsd` library.

The last column uses two terms:

- **Reads 0:** the series is present with the value 0. The library gives an
  unreported field its zero value, so this looks the same as a real 0.
- **Omitted:** the series is missing from the response, along with its `HELP`
  and `TYPE` lines.

## `apcupsd_*`

| Metric | Type | Unit | Labels | NIS field | When the UPS doesn't report it |
|---|---|---|---|---|---|
| `apcupsd_info` | gauge | none, always 1 | `ups`, `hostname`, `model`, `status` | `UPSNAME`, `HOSTNAME`, `MODEL`, `STATUS` | Present, with that label empty |
| `apcupsd_ups_load_percent` | gauge | percent, 0 to 100 | `ups` | `LOADPCT` | Reads 0 |
| `apcupsd_battery_charge_percent` | gauge | percent, 0 to 100 | `ups` | `BCHARGE` | Reads 0 |
| `apcupsd_line_volts` | gauge | volts | `ups` | `LINEV` | Reads 0 |
| `apcupsd_line_nominal_volts` | gauge | volts | `ups` | `NOMINV` | Reads 0 |
| `apcupsd_output_volts` | gauge | volts | `ups` | `OUTPUTV` | Reads 0 |
| `apcupsd_output_amps` | gauge | amps | `ups` | `OUTCURNT` | Omitted, and also omitted on a reading of 0 |
| `apcupsd_battery_volts` | gauge | volts | `ups` | `BATTV` | Reads 0 |
| `apcupsd_battery_nominal_volts` | gauge | volts | `ups` | `NOMBATTV` | Reads 0 |
| `apcupsd_battery_number_transfers_total` | counter | transfers | `ups` | `NUMXFERS` | Reads 0 |
| `apcupsd_battery_time_left_seconds` | gauge | seconds | `ups` | `TIMELEFT` | Reads 0 |
| `apcupsd_battery_time_on_seconds` | gauge | seconds | `ups` | `TONBATT` | Reads 0 |
| `apcupsd_battery_cumulative_time_on_seconds_total` | counter | seconds | `ups` | `CUMONBATT` | Reads 0 |
| `apcupsd_last_transfer_on_battery_time_seconds` | gauge | UNIX time, seconds | `ups` | `XONBATT` | Reads 0, and so does `N/A` |
| `apcupsd_last_transfer_off_battery_time_seconds` | gauge | UNIX time, seconds | `ups` | `XOFFBATT` | Reads 0, and so does `N/A` |
| `apcupsd_last_selftest_time_seconds` | gauge | UNIX time, seconds | `ups` | `LASTSTEST` | Reads 0, and so does `N/A` |
| `apcupsd_nominal_power_watts` | gauge | watts | `ups` | `NOMPOWER` | Reads 0 |
| `apcupsd_internal_temperature_celsius` | gauge | degrees Celsius | `ups` | `ITEMP` | Reads 0 |

## Notes

- **`apcupsd_output_amps` is the only metric that can be missing.** It is sent
  only when the value is above 0, because a constant 0 would be misleading on a
  UPS that doesn't report `OUTCURNT`. The library can't tell that case from a
  real reading of 0, so a true 0 A reading is omitted as well, and a UPS that
  does report the field can gain and lose the series as its load changes.
- **A 0 from any other metric doesn't prove the UPS measured 0.** For example,
  `apcupsd_internal_temperature_celsius` reads 0 on a model with no temperature
  sensor.
- **`ups` comes from `UPSNAME` on every metric.** A UPS that doesn't report it
  gets `ups=""`, which Prometheus treats the same as no `ups` label.
- **`status` is the raw `STATUS` text,** so each change of state starts a new
  `apcupsd_info` series.
- **`TIMELEFT` is sent in minutes** and exported in seconds. `TONBATT` and
  `CUMONBATT` are sent in seconds.
- **The two counters count from apcupsd startup,** so they reset when the daemon
  restarts.

## When a scrape fails

Failure is all-or-nothing. In each of these cases the whole response is an HTTP
500, including the `go_*` and `process_*` families, and Prometheus records
`up == 0`:

- apcupsd can't be reached, or doesn't finish answering within 5 seconds.
- Any NIS field fails to parse, even one no metric uses, such as `LOTRANS`. That
  covers:
  - a number field that holds something else, including `N/A`
  - a `NOMPOWER` or `NUMXFERS` value that isn't a whole number
  - a `TIMELEFT`, `TONBATT` or `CUMONBATT` value with a missing or unknown unit,
    such as `5` or `1 Hours`
  - a timestamp that is neither `YYYY-MM-DD HH:MM:SS +ZZZZ` nor `N/A`
  - a line with no colon
- A single NIS message is over 256 bytes. This panics in the library. The panic
  is recovered, so only that scrape fails and the exporter keeps running.

Two kinds of field can't fail a scrape: unknown keys are ignored, and so is an
unparsable `ALARMDEL`.

## `go_*`, `process_*` and `promhttp_*`

These families are only present with `-metrics.system=true`, which is the
default. With `-metrics.system=false`, the response has `apcupsd_*` metrics and
nothing else.

| Family | Source |
|---|---|
| `go_*` | The Go runtime, through the `client_golang` Go collector |
| `process_*` | The operating system, through the `client_golang` process collector. The set depends on the platform: macOS has no `process_network_*`, for example |
| `promhttp_metric_handler_requests_total`, `promhttp_metric_handler_requests_in_flight` | Requests to the metrics path |

Unlike `apcupsd_*`, these aren't frozen. They can change with any Go or
`client_golang` upgrade.
