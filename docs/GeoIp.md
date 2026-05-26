# GeoIP

## Data source

The master enriches BGP IP lookups with location and operator data from an ipinfo Lite CSV file. The file is not included in the repository and must be obtained separately from ipinfo.io.

Accepted formats: plain CSV (`.csv`) or gzip-compressed CSV (`.csv.gz`).

## CSV schema

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

## Internal representation

The loader makes a single pass over the CSV and builds two structures:

**Radix trie** — same binary trie implementation used for BGP prefix lookup. IP-to-record lookup is O(32) for IPv4 and O(128) for IPv6.

**ASN index** — `map[string]*Record` keyed by ASN string (`AS15169`). Built alongside the trie. Used for O(1) operator name resolution when enriching AS path hops. Only the first record seen for each ASN is stored.

## Updating

Replace the file at `GEOIP_PATH` and restart the service. There is no hot-reload for GeoIP data — a restart is required.