# BGP Data

## Format

The Master loads BGP routes from a flat JSON file produced by the private
`mrt2json` converter. It never reads raw MRT data directly.

```json
{
  "timestamp": 1746000000,
  "routes": [
    {
      "prefix":      "1.0.0.0/24",
      "nexthop":     "80.77.16.114",
      "aspath":      [13335, 15169],
      "origin":      "igp",
      "localpref":   100,
      "med":         0,
      "communities": ["13335:10000"]
    }
  ]
}
```

The current Master decodes but does not use the top-level `timestamp` for
`/api/info`. Its `bgp_updated` field reports when the active snapshot was
loaded or reloaded by the Master, not the MRT capture or provider timestamp.

## Data flow and path roles

The source file and converter location are operator-selected. The generated
JSON path is selected by `BGP_DATA_PATH`:

```text
<bgp-source-dir>/latest-bview.gz
    |
    v
  mrt2json
    |
    v
BGP_DATA_PATH
    |
    v
Master BGP store
```

`latest-bview.gz` is the raw/provider input to the private converter.
`BGP_DATA_PATH` identifies the authoritative generated runtime artifact that
the Master consumes. Its implementation-defined default is:

```ini
BGP_DATA_PATH=/var/lib/looking-glass/bgp.json
```

The private converter consumes the raw file; the Master consumes only the
JSON. See [INSTALL.md](../INSTALL.md#filesystem-and-data-layout) for the
filesystem roles and source-defined defaults.

## Obtaining MRT data

RIPE RIS provides full RIB snapshots updated every eight hours. Obtain the
selected snapshot from the provider and transfer it to the system that runs
the converter if necessary:

```sh
wget https://data.ris.ripe.net/rrc00/latest-bview.gz
```

Other collectors: rrc01 through rrc26. Each collector has a different set of peers and may provide different path diversity. rrc00 (Amsterdam) is a reasonable default for a single-collector setup.

RouteViews is an alternative source with different peer coverage:

```sh
wget http://archive.routeviews.org/bgpdata/$(date +%Y.%m)/RIBS/rib.$(date +%Y%m%d).0000.bz2
```

## Converting

`mrt2json` is private operator-side tooling whose implementation is not
distributed in the public source checkout. Its operational interface is:

```sh
mrt2json <bgp-source-dir>/latest-bview.gz <bgp-json-path>
```

Set `BGP_DATA_PATH` to `<bgp-json-path>`, or write to the built-in default
`/var/lib/looking-glass/bgp.json`.

The converter reads TABLE_DUMP2 format, deduplicates prefixes (first-seen peer
wins), skips malformed records, and replaces the requested JSON through a
destination-adjacent `.tmp` file and rename. That `.tmp` file is temporary
staging, not an alternative runtime artifact. Processing a full table takes
10–15 minutes.

Output size: ~260 MB for a full global table (~1.4M unique prefixes).

## Hot-reload

The Master polls the BGP JSON selected by `BGP_DATA_PATH` every five minutes.
When its mtime advances, the file is re-read and a new snapshot is built
before publication. The old snapshot continues serving requests until the new
one is atomically swapped in via `sync/atomic.Pointer`. A load failure leaves
the old snapshot active. There is no downtime during a successful reload.

A normal BGP data refresh requires no Master restart solely for the data
update. Replacing `latest-bview.gz` alone also causes no reload because the
Master does not consume that file; conversion and publication of `bgp.json`
must complete first.

## Updating on a schedule

The complete update procedure is:

1. Obtain the selected MRT snapshot from the provider.
2. Place or replace it at an operator-selected raw-input path such as
   `<bgp-source-dir>/latest-bview.gz`.
3. Run the operator-supplied private `mrt2json` converter with that raw input
   and the JSON selected by `BGP_DATA_PATH` as its output.
4. Allow the Master to detect the successfully replaced JSON and reload it on
   the five-minute polling cycle.

Do not create persistent variants such as `bgp-final.json`,
`bgp-new-final.json`, or `bgp-old2.json`. The authoritative runtime path
is the path selected by `BGP_DATA_PATH`; its built-in default is
`/var/lib/looking-glass/bgp.json`.

## Why next-hop is not shown

A selected RIPE RIS collector can contain routes from multiple peers. The
converter keeps the first record it encounters for each prefix, so the stored
MRT next-hop belongs to the peer whose record was retained for that prefix.
It is not a meaningful forwarding next-hop from the Looking Glass Master's
perspective, and displaying it would imply otherwise. The kernel FIB
(`ip route get`) was evaluated as an alternative, but a host with only a
default route returns its gateway for every destination. AS path, origin, and
communities are shown instead.
