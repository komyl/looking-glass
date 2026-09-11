# GeoIP

## Current Master runtime

The Master loads one published canonical GeoIP CSV or CSV.GZ artifact through
`GEOIP_PATH`. The artifact is prepared offline from MaxMind Country, MaxMind
ASN, and IPinfo Lite inputs. It is not included in the repository.

Accepted formats: plain CSV (`.csv`) or gzip-compressed CSV (`.csv.gz`).
The Master does not read MaxMind MMDB files directly.

Provider paths are operator-selected. For example, one source directory can
contain:

- `<geoip-source-dir>/GeoLite2-Country.mmdb`;
- `<geoip-source-dir>/GeoLite2-ASN.mmdb`;
- `<geoip-source-dir>/ipinfo_lite.csv.gz`.

Only the private `geoipbuilder` consumes the three provider inputs. The Master
consumes only the published canonical artifact selected by `GEOIP_PATH`. See
[INSTALL.md](../INSTALL.md#filesystem-and-data-layout) for filesystem roles
and source-defined defaults.

## Runtime and canonical CSV schema

```
network,country,country_code,continent,continent_code,asn,as_name,as_domain
1.0.0.0/24,Australia,AU,Oceania,OC,AS13335,"Cloudflare, Inc.",cloudflare.com
```

## Configuration

Set the published artifact path explicitly:

```ini
GEOIP_PATH=<published-canonical-geoip-path>
```

`GEOIP_PATH2` is no longer supported. Any non-empty value is invalid
configuration and stops Master startup explicitly; it is not ignored or used
as a second source. Keep it unset. The source retains
`/var/lib/looking-glass/ipinfo_lite.csv.gz` as a legacy fallback when
`GEOIP_PATH` is unset. Despite that filename, the fallback must contain the
same canonical schema; it is not an IPinfo provider input or a second runtime
source.

## Runtime loading boundary

```text
GEOIP_PATH
→ one published canonical CSV/CSV.GZ artifact
→ strict all-or-nothing structural load
→ Master GeoIP DB
```

The runtime loader requires the exact header above, exactly eight fields in
every record, valid CIDR syntax in every `network` field, and complete
CSV/GZIP consumption. Physical blank lines outside quoted CSV data are also
rejected. It builds the trie and ASN index in an unpublished snapshot and
installs the database only after the complete artifact reaches clean EOF.

If the artifact is missing, unreadable, or fails one of these structural
checks, the complete GeoIP database is rejected. The Master logs the failure
and continues with GeoIP disabled. BGP route lookup still works, per-route
`geo` is absent, the successful BGP response retains
`"aspath_enriched": null`, and `/api/ip-info` returns `{}`.

The Master does not re-read the provider sources or independently establish
source precedence, source-boundary coverage, canonical ordering,
duplicate/overlap rejection, deterministic generation, source-backed field
correctness, artifact identity, or publication integrity. Those guarantees
belong to the offline validation and publication boundary below.

## Offline canonical candidate generation

The private operator-side `geoipbuilder` tool combines three local inputs:

- MaxMind GeoLite2 Country MMDB;
- MaxMind GeoLite2 ASN MMDB;
- IPinfo Lite CSV or CSV.GZ.

Its implementation under `cmd/` is not distributed in the public GitHub
checkout, but its operational interface is:

```sh
geoipbuilder \
  -country <geoip-source-dir>/GeoLite2-Country.mmdb \
  -asn <geoip-source-dir>/GeoLite2-ASN.mmdb \
  -ipinfo <geoip-source-dir>/ipinfo_lite.csv.gz \
  -output <temporary-candidate.csv.gz>
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
does not expand address space one IP at a time. Choose a temporary
operator-controlled candidate path. The builder refuses to overwrite it if it
already exists, and its directory must satisfy the builder's path-safety
requirements.

Candidate generation is offline only. It does not replace a published
artifact, change `GEOIP_PATH`, or alter the running Master.

## Offline candidate validation and publication

Validation requires the original three sources and a completed candidate:

```sh
geoipbuilder \
  -country <geoip-source-dir>/GeoLite2-Country.mmdb \
  -asn <geoip-source-dir>/GeoLite2-ASN.mmdb \
  -ipinfo <geoip-source-dir>/ipinfo_lite.csv.gz \
  -candidate <temporary-candidate.csv.gz>
```

The validator rejects malformed or truncated CSV/GZIP, a non-exact header or
field count, non-canonical CIDRs, duplicates, overlaps, ordering errors, and
trailing data. It independently walks the source boundaries, derives field
precedence, and requires exact maximal CIDR coverage and field values. It
reports SHA-256 identities for both the exact artifact bytes and canonical
uncompressed CSV content.

Add `-publish` to replace an offline published artifact only after validation:

```sh
geoipbuilder \
  -country <geoip-source-dir>/GeoLite2-Country.mmdb \
  -asn <geoip-source-dir>/GeoLite2-ASN.mmdb \
  -ipinfo <geoip-source-dir>/ipinfo_lite.csv.gz \
  -candidate <temporary-candidate.csv.gz> \
  -publish <published-canonical-geoip-path>
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

Candidate validation and publication do not alter the running Master. A
successfully published artifact becomes the runtime input only when
`GEOIP_PATH` names it and the Master is restarted.

The candidate and the destination-local publication staging file are
temporary, non-authoritative working state. They do not belong in the
persistent filesystem model, and the workflow does not retain an older
published generation after a successful rename. Do not accumulate `.old`,
`.backup`, `.final2`, or similar copies in the canonical GeoIP directory.

## Internal representation

The loader makes a single pass over the CSV and builds two structures:

**Radix trie** — a separate trie implementation following the same general
binary-radix design as BGP prefix lookup. IP-to-record lookup is O(32) for
IPv4 and O(128) for IPv6.

**ASN index** — `map[string]*Record` keyed by ASN string (`AS15169`). Built alongside the trie. Used for O(1) operator name resolution when enriching AS path hops. Only the first record seen for each ASN is stored.

## Updating the current runtime

The complete update procedure is:

1. Replace or update the three provider inputs at their operator-selected
   paths.
2. Build a temporary candidate with the private `geoipbuilder`.
3. Validate the completed candidate independently against the same three
   provider inputs.
4. Publish the validated bytes fail-closed and atomically to
   `<published-canonical-geoip-path>`.
5. Set `GEOIP_PATH=<published-canonical-geoip-path>` and ensure `GEOIP_PATH2`
   is unset.
6. Restart the Master, then verify successful startup and GeoIP loading.
7. Remove the temporary candidate after successful publication and
   verification.

There is no hot reload for GeoIP data. Replacing provider inputs or publishing
the canonical artifact does not by itself change the in-memory runtime DB.
