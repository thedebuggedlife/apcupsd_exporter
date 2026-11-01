# Metrics

Every `apcupsd_*` metric the exporter serves, where it comes from, and what
happens when the UPS doesn't report it. `TestMetricsCatalogueMatchesDescribe`
keeps this file and the code in step: a metric with no row here, or a name here
that the exporter doesn't describe, fails the tests.

The NIS field is the key shown by `apcaccess status`. Each value is parsed by
the `github.com/mdlayher/apcupsd` library, except that the exporter reads the
text of `BATTDATE` and `STATFLAG` itself.

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
| `apcupsd_status_online` | gauge | 1 or 0 | `ups` | `STATFLAG`, or `STATUS` | Omitted, see [Status gauges](#status-gauges) |
| `apcupsd_status_on_battery` | gauge | 1 or 0 | `ups` | `STATFLAG`, or `STATUS` | Omitted, see [Status gauges](#status-gauges) |
| `apcupsd_status_low_battery` | gauge | 1 or 0 | `ups` | `STATFLAG`, or `STATUS` | Omitted, see [Status gauges](#status-gauges) |
| `apcupsd_line_frequency_hertz` | gauge | hertz | `ups` | `LINEFREQ` | Omitted, and also omitted on a reading of 0 |
| `apcupsd_battery_date_time_seconds` | gauge | UNIX time, seconds | `ups` | `BATTDATE` | Omitted, and also omitted unless the value is `YYYY-MM-DD` |
| `apcupsd_last_transfer_on_battery_reason` | gauge | none, always 1 | `reason`, `ups` | `LASTXFER` | Omitted |
| `apcupsd_exporter_build_info` | gauge | none, always 1 | `version` | None, set when the exporter is built | Always present on a successful scrape |

## Notes

- **`apcupsd_output_amps` and `apcupsd_line_frequency_hertz` are sent only when
  the value is above 0,** because a constant 0 would be misleading on a UPS that
  doesn't report the field. The library can't tell that case from a real reading
  of 0, so a true 0 is omitted as well, and a UPS that does report `OUTCURNT` can
  gain and lose the series as its load changes.
- **A 0 from a metric marked "Reads 0" doesn't prove the UPS measured 0.** For
  example, `apcupsd_internal_temperature_celsius` reads 0 on a model with no
  temperature sensor.
- **`ups` comes from `UPSNAME` on every metric that has it.** A UPS that doesn't
  report it gets `ups=""`, which Prometheus treats the same as no `ups` label.
- **`status` is the raw `STATUS` text,** so each change of state starts a new
  `apcupsd_info` series.
- **`reason` is the raw `LASTXFER` text,** so each new reason starts a new
  `apcupsd_last_transfer_on_battery_reason` series. It describes the most recent
  transfer, including `No transfers since turnon`, which isn't a transfer. The
  value says nothing about when it happened: use
  `apcupsd_last_transfer_on_battery_time_seconds` for that.
- **`BATTDATE` is the date the UPS reports for its battery.** On a USB UPS this
  can be the battery's manufacture date, not the date it was replaced. It has no
  time of day, so `apcupsd_battery_date_time_seconds` is midnight UTC on that
  date. To alert on age, use
  `time() - apcupsd_battery_date_time_seconds > 3 * 365 * 86400`. A `BATTDATE`
  with slashes is omitted, because apcupsd allows both `mm/dd/yy` and `dd/mm/yy`
  and nothing shows which one a UPS uses.
- **`version` on `apcupsd_exporter_build_info`** is the image tag for a release,
  such as `v0.5.0`, `edge` for a build from `main`, and `dev` for any other build.
  A release commit is published from both, so an image pulled by its full commit
  SHA can show either one.
- **`TIMELEFT` is sent in minutes** and exported in seconds. `TONBATT` and
  `CUMONBATT` are sent in seconds.
- **The two counters count from apcupsd startup,** so they reset when the daemon
  restarts.

## Status gauges

The three status gauges use the bits in `STATFLAG`, because apcupsd
replaces all of the `STATUS` text with `SHUTTING DOWN` during a shutdown, while
the bits stay correct. If `STATFLAG` is missing, unreadable or shows neither
online nor on battery, they use the words `ONLINE`, `ONBATT` and `LOWBATT` from
`STATUS` instead.

All three are sent together, and all three are omitted when the state isn't
known:

- apcupsd has lost contact with the UPS, shown by either `STATUS` or `STATFLAG`.
  The `STATFLAG` bits are out of date in that state.
- Neither `STATFLAG` nor `STATUS` shows online or on battery. That includes a
  scrape just after apcupsd starts, before it has read the UPS.

Because they can be omitted, an alert on `apcupsd_status_on_battery == 1` resolves
if contact is lost during an outage. Pair it with `absent()` if that matters.

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

Three kinds of field can't fail a scrape: unknown keys are ignored, so is an
unparsable `ALARMDEL`, and an unusable `BATTDATE` or `STATFLAG` only omits the
metrics that use it.

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
