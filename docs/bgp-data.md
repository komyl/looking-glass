# BGP Data

## Format

The master loads BGP routes from a flat JSON file produced by the `mrt2json` converter.

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

## Obtaining MRT data

RIPE RIS provides full RIB snapshots updated every 8 hours:

```sh
wget https://data.ris.ripe.net/rrc00/latest-bview.gz
```

Other collectors: rrc01 through rrc26. Each collector has a different set of peers and may provide different path diversity. rrc00 (Amsterdam) is a reasonable default for a single-collector setup.

RouteViews is an alternative source with different peer coverage:

```sh
wget http://archive.routeviews.org/bgpdata/$(date +%Y.%m)/RIBS/rib.$(date +%Y%m%d).0000.bz2
```

## Converting

```sh
./mrt2json latest-bview.gz /var/lib/looking-glass/bgp.json
```

The converter reads TABLE_DUMP2 format, deduplicates prefixes (first-seen peer wins), skips malformed records, and writes the JSON file atomically (write to `.tmp`, then rename). Processing a full table takes 10–15 minutes.

Output size: ~260 MB for a full global table (~1.4M unique prefixes).

## Hot-reload

The master polls the file's mtime every 5 minutes. When the mtime advances, the file is re-read and a new snapshot is built. The old snapshot continues serving requests until the new one is atomically swapped in via `sync/atomic.Pointer`. There is no downtime during reload.

To force an immediate reload:

```sh
systemctl restart looking-glass
```

## Updating on a schedule

The master node has no external internet access. BGP data must be obtained externally and transferred to the server. A typical workflow:

1. On an external machine with internet access, download the latest MRT dump and run `mrt2json`.
2. Transfer the resulting `bgp.json` to `/var/lib/looking-glass/bgp.json` on the master.
3. The service reloads automatically within 5 minutes.

## Why next-hop is not shown

The MRT dump is collected from a single RIPE RIS peer. Every route's next-hop is the address of that peer, not a routing-relevant address from the perspective of the server running the looking glass. Displaying it would suggest it means something it does not. The kernel FIB (`ip route get`) was evaluated as an alternative but a VPS has only a default route — it returns the gateway IP for every destination. AS Path, origin, and communities are shown instead.