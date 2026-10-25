# Initial build verification — 2026-10-25

## Automated checks

`./scripts/verify-release.sh 0.1.0` passes:

- `go vet ./...`
- `go test -race ./...`
- JavaScript syntax check
- SDK manifest CLI loading and catalog presentation validation

The tests exercise real local HTTP emulators through the engine and SDK handler. They cover endpoint identity collisions, explicit station failover on HTTP 503, idempotent reservations and viewer ownership, concurrent tuner capacity, configured-host admission, private gzip XMLTV caching, offline restart, ambiguous guide matching, malformed guide rejection, filler classification, HLS URI rewriting, stop cancellation, HEAD behavior, lease expiry, and the Live TV/browser route contract.

The companion Silo `internal/plugins` package passes with the race detector. Its relay tests show that bytes arrive before an unbounded producer finishes, client disconnect cancels the producer, caller cookies/bearer tokens and upstream cookies do not cross the relay boundary, and public routes, redirects, DNS targets, and non-loopback targets are refused. A regression test also verifies the validated launch profile is passed to plugins instead of a forged incoming profile header.

## Browser checks

The development harness serves two actual HTTP emulators plus the real plugin HTTP handlers. An FFmpeg-generated H.264/AAC clip was used; these are test channels/programmes, not a live television lineup.

- Loaded two independent lineups, six channels each, with separate channel identities.
- Filtered to one source and browsed three-hour programme windows.
- Added a favorite through the Silo-settings-shaped API.
- Edited and saved an endpoint name through the settings/config flow; the updated endpoint appeared after refresh.
- Played HLS: media element `readyState=4`, `paused=false`, playback time advanced beyond five seconds.
- Played MPEG-TS through Mpegts.js: `readyState=4`, `paused=false`, playback time advanced beyond twelve seconds.
- Stopped MPEG-TS from the UI and observed both endpoints reporting zero reservations.
- Checked 1280px desktop and 390px mobile. Fixed horizontal overflow; mobile document width then equaled the 390px viewport.
- No browser JavaScript errors were reported during those checks.

The T3 preview initialized, but snapshots timed out twice; browser QA used the documented agent-browser fallback.

## Packages

`./scripts/build-release.sh` builds Linux amd64, Linux arm64, and macOS arm64. Each upload ZIP contains the executable and a complete SDK manifest. Checksums, version, plugin ID, route/config metadata, and executable permissions were checked. Each package manifest declares its own target platform; the source manifest lists all release targets.

## Remaining live validation

No physical HDHomeRun was available. Hardware tuning delays, broadcast reception, SiliconDust guide authorization, protected channels, and real OTA codec compatibility remain unverified.

Read-only requests from the Silo server to the user's existing Dispatcharr `/hdhr/discover.json` and `/hdhr/lineup.json` both returned **HTTP 403**. No access-control settings or service configuration were changed. Live Dispatcharr testing needs those endpoints to admit the Silo server first.

Browser QA used a development host adapter rather than a running full Silo installation. Silo's production HTTP relay is separately covered at its real HTTP proxy boundary. An installation/launch smoke test on the updated Silo fork remains part of deployment acceptance.

This build has not been published to GitHub, added to the catalog, or deployed to a server.
