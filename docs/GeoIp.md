# GeoIP

## Current Master runtime

The master enriches BGP IP lookups with location and operator data from an ipinfo Lite CSV file. The file is not included in the repository and must be obtained separately from ipinfo.io.

Accepted formats: plain CSV (`.csv`) or gzip-compressed CSV (`.csv.gz`).
The Master does not read MaxMind MMDB files directly.

## Runtime and canonical CSV schema

```
network,country,country_code,continent,continent_code,asn,as_name,as_domain
1.0.0.0/24,Australia,AU,Oceania,OC,AS13335,"Cloudflare, Inc.",cloudflare.com
```

## Configuration

Set the `GEOIP_PATH` environment variable in the master's service unit:

```
Environment=GEOIP_PATH=/opt/ipinfo/ipinfo_lite.csv.gz
```

Multiple sources may be specified using `GEOIP_PATH` and `GEOIP_PATH2`.
The second source takes precedence for fields present in both.

Merging is performed per-field. Only non-empty fields from a later source override values from earlier sources. Empty fields are left unchanged.

If the file is missing or unreadable at startup, the master logs a warning and continues without GeoIP. BGP lookups still work; the `geo` and `aspath_enriched` fields are omitted from responses.

## Offline canonical candidate generation

The private operator-side `geoipbuilder` tool combines three local inputs:

- MaxMind GeoLite2 Country MMDB;
- MaxMind GeoLite2 ASN MMDB;
- IPinfo Lite CSV or CSV.GZ.

Its implementation under `cmd/` is not distributed in the public GitHub
checkout, but its operational interface is:

```sh
geoipbuilder -country <GeoLite2-Country.mmdb> \
  -asn <GeoLite2-ASN.mmdb> \
  -ipinfo <ipinfo_lite.csv|ipinfo_lite.csv.gz> \
  -output <candidate.csv|candidate.csv.gz>
```

The output uses the eight-column schema above. Logically, `network` is the
canonical prefix field serialized under the name already accepted by the
Master's CSV loader.

Precedence is applied independently per field:

- `country`, `country_code`, `continent`, and `continent_code`: a non-empty
  MaxMind Country value, then IPinfo, then empty;
- `asn` and `as_name`: a present/non-empty MaxMind ASN value, then IPinfo,
  then empty;
- `as_domain`: IPinfo only.

A present numeric MaxMind ASN of zero is retained as `AS0`. The builder never
synthesizes `as_domain`, and operator-name equality is not used as ASN
identity.

Canonical partitioning uses the union of all selected source-prefix
boundaries. A generated prefix therefore never crosses a Country, ASN, or
IPinfo boundary, even when those sources use different prefix lengths. IPv4
and IPv6 are processed separately. Bare IPv4 and IPv6 hosts become `/32` and
`/128`; non-network CIDRs and malformed, overlapping, or out-of-order source
records fail the build.

Output is deterministic, ordered, canonical, unique, and non-overlapping. The
builder streams IPinfo and output records, uses bounded resource controls, and
does not expand address space one IP at a time. It refuses to overwrite an
existing requested candidate path.

Candidate generation is offline only. It does not replace current GeoIP data,
set `GEOIP_PATH` or `GEOIP_PATH2`, or alter the running Master.

## Offline candidate validation and publication

Validation requires the original three sources and a completed candidate:

```sh
geoipbuilder -country <GeoLite2-Country.mmdb> \
  -asn <GeoLite2-ASN.mmdb> \
  -ipinfo <ipinfo_lite.csv|ipinfo_lite.csv.gz> \
  -candidate <candidate.csv|candidate.csv.gz>
```

The validator rejects malformed or truncated CSV/GZIP, a non-exact header or
field count, non-canonical CIDRs, duplicates, overlaps, ordering errors, and
trailing data. It independently walks the source boundaries, derives field
precedence, and requires exact maximal CIDR coverage and field values. It
reports SHA-256 identities for both the exact artifact bytes and canonical
uncompressed CSV content.

Add `-publish` to replace an offline published artifact only after validation:

```sh
geoipbuilder -country <GeoLite2-Country.mmdb> \
  -asn <GeoLite2-ASN.mmdb> \
  -ipinfo <ipinfo_lite.csv|ipinfo_lite.csv.gz> \
  -candidate <candidate.csv|candidate.csv.gz> \
  -publish <published.csv|published.csv.gz>
```

Candidate and published paths must use the same CSV or CSV.GZ format. The
published path must not name the same filesystem object as any source or the
candidate; existing aliases through equivalent paths, symbolic links, or hard
links are rejected before staging. The tool copies the exact candidate bytes
into a destination-local staging file while validating the already-open
candidate. It syncs and closes that file, then atomically renames it over the
published path and syncs the directory. The old published artifact remains
until rename, which is the commit point. No older generation is retained after
commit, and concurrent successful publishers use last-rename-wins semantics.

Candidate validation and publication still do not alter the running Master.
The runtime canonical-source switch is not implemented.

## Internal representation

The loader makes a single pass over the CSV and builds two structures:

**Radix trie** — same binary trie implementation used for BGP prefix lookup. IP-to-record lookup is O(32) for IPv4 and O(128) for IPv6.

**ASN index** — `map[string]*Record` keyed by ASN string (`AS15169`). Built alongside the trie. Used for O(1) operator name resolution when enriching AS path hops. Only the first record seen for each ASN is stored.

## Updating the current runtime

Replace the file at `GEOIP_PATH` and restart the service. There is no hot-reload for GeoIP data — a restart is required.
