# HDHomeRun for Silo

Live TV from multiple physical or emulated HDHomeRun HTTP endpoints, built for the **Ramindex Silo fork**. Uses Silo Plugin SDK **v0.30.0** and the fork's `livetv/v1` contract.

- Explicit endpoint addresses, including URL path prefixes used by emulators.
- Distinct channel IDs per endpoint, even when device IDs and channel numbers collide.
- Optional station mappings combine equivalent channels, with priority and tuner-aware failover before playback starts.
- Per-endpoint tuner reservations, atomic capacity checks, 90-second leases, viewer ownership, heartbeats, and stop/disconnect cleanup.
- XMLTV/XMLTV.gz or the SiliconDust guide; retained lineup and guide during outages.
- Silo-themed channel browsing, a three-hour timeline guide, profile favorites, admin endpoint health and channel mapping.
- The existing Live TV navigation and source sidebar, with 60-channel pages to keep large lineups responsive.
- MPEG-TS and HLS playback through a private loopback gateway. HLS playlists, keys, nested playlists, and media stay behind Silo authentication.

## Install and configure

1. Build the companion Silo fork with the generic loopback HTTP stream relay in `internal/plugins/http_proxy_relay.go`. Native compatible TV clients use the existing Live TV contract; the embedded browser player needs this relay.
2. Upload the matching `dist/hdhomerun-0.2.0-<os>-<arch>.silo-plugin.zip` in Silo's plugin administration.
3. Open **HDHomeRun Devices**, add a name and base address, then save. For a real tuner, use `http://192.168.1.30`. For Dispatcharr, use its HDHR base, normally `https://your-dispatcharr/hdhr`, or its exported channel/output profile base.
4. The Silo server must reach both `discover.json` and the advertised lineup and media URLs. A reverse proxy or the source's network access rules must admit the server.
5. Choose **No guide**, **SiliconDust guide**, or **XMLTV URL** independently for each endpoint. An emulator normally uses XMLTV. You may instead put a guide URL in a server-side file and set **XMLTV URL file**; never configure both.
6. Automatic tuner count uses `TunerCount`, with a conservative fallback of one. Set a lower override if other applications share the tuner. Reservations account for this plugin's streams; a device's busy response also triggers failover.

The lineup refreshes every five minutes. XMLTV refreshes every six hours; SiliconDust refreshes randomly every 20–28 hours, using a fresh discovery authorization value. The manual/scheduled refresh checks endpoint health and refreshes guides when due. Guide errors retry after 30 minutes. SiliconDust currently offers three days without an account and fourteen days with a paid DVR subscription; see its [XMLTV documentation](https://info.hdhomerun.com/info/dvr:xmltv).

Configuration belongs to Silo under global key `connection`; the UI validates it before saving through Silo's admin API. No separate credential file is generated. Private catalog caching defaults to `/var/lib/continuum/plugins/silo.ramindex.hdhomerun`; override with `HDHOMERUN_DATA_DIR`. Give the plugin process write access to that directory. Device authorization is neither returned in viewer APIs nor saved in the cache.

## Combining stations

Edit each endpoint and expand **Channel names, guide matching, and failover**. Assign the same station key to channels carrying the same station. Lower endpoint priority wins. A busy or unavailable endpoint can move the reserved session to another matching endpoint. Separate sources are never merged by channel name or number automatically.

Keep endpoint IDs stable when changing an address or display name; the editor preserves them. A station mapping intentionally changes the channel ID to the shared station identity, so existing favorites for the separate channel need selecting again.

Guide IDs override automatic matching. Otherwise an exact guide ID/number or an unambiguous display name is used. Ambiguous names stay unmatched. Hidden channels can be restored from their saved mapping even when absent from the visible lineup.

## Playback limits

- This is an HTTP HDHomeRun client. Add addresses explicitly; UDP/SSDP/mDNS subnet discovery and legacy UDP-only devices are outside this release.
- DRM/protected channels are listed but cannot play. This plugin does not bypass protection.
- No DVR, recording scheduler, or timeshift cache is included.
- The plugin relays source codecs. It does **not** transcode MPEG-2, AC-3, HEVC, or AC-4 for browsers. Use source-side conversion or a compatible TV client when the browser cannot decode the source.
- Failover happens before bytes are sent. An interrupted stream needs restarting; there is no seamless midstream source switching.
- Silo and its plugin process must share a network namespace and local listener. This release does not coordinate tuner reservations across multiple Silo nodes or separate installations.
- A device or emulator can advertise its configured hostname and its resolved IP aliases. Other media hosts require an explicit additional-host entry. Cross-host redirects are checked. Link-local, multicast, and unspecified targets are refused.

## Development

Requires Go 1.26 and Node 22+ for verification.

```sh
./scripts/verify-release.sh
./scripts/build-release.sh
# Two local emulators, a local admin identity, in-memory settings:
go run ./cmd/dev
# A larger lineup for paging/layout checks:
go run ./cmd/dev -channels 120
```

The dev harness binds only a loopback listener and must never be deployed. Without `-media-dir`, it exercises setup/lineup/guide and reports stream unavailability. For actual browser video, generate HLS and a TS fixture with FFmpeg:

```sh
mkdir -p .tmp-media
ffmpeg -f lavfi -i testsrc2=size=640x360:rate=24 -f lavfi -i sine=frequency=440:sample_rate=48000 -t 16 -c:v libx264 -preset ultrafast -g 48 -c:a aac -f hls -hls_time 2 -hls_list_size 0 -hls_segment_filename .tmp-media/segment-%03d.ts .tmp-media/index.m3u8
ffmpeg -i .tmp-media/index.m3u8 -c copy -f mpegts .tmp-media/sample.ts
go run ./cmd/dev -media-dir .tmp-media
```

Builds produce Linux amd64, Linux arm64, and macOS arm64 binaries, checksums, manifests, and Silo upload ZIPs. The configured Release workflow uses the Ramindex shared release pipeline. No repository, release, or catalog entry is created by a local build.

Protocol references: [HDHomeRun HTTP API](https://info.hdhomerun.com/info/http_api), [SiliconDust XMLTV](https://info.hdhomerun.com/info/dvr:xmltv), [Dispatcharr HDHR routes](https://github.com/Dispatcharr/Dispatcharr/blob/main/apps/hdhr/urls.py). See [verification](docs/verification.md) and [third-party notices](THIRD_PARTY_NOTICES.md).
