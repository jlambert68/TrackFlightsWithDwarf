# TrackFlights

TrackFlights is the unified Go web application for the SDR flight tracker and
DWARF object-tracking workflows. The current implementation establishes the
server, embedded React UI, settings store, job model, and migrated SDR core
packages.

## Run

```sh
make run
```

Open `http://127.0.0.1:8080`. Runtime data defaults to `./data`; set
`TRACKFLIGHTS_DATA_ROOT` to use another location.

The service intentionally starts without SDR or DWARF hardware. `POST
/api/v1/flights/frames` accepts a JSON object with a `frame` field containing
one `rtl_adsb` `*HEX;` frame, which supports fixture-driven development.

## Validate

```sh
make test
```

Run `make help` to list all targets. `make build` writes the production binary
to `bin/trackflights`; `make frontend-install` installs the locked frontend
dependencies.

See [MIGRATION_PLAN.md](MIGRATION_PLAN.md) for the remaining staged migration
work and feature-parity criteria.

## Run both services locally

Run `make run-local` to build and start both the SDR service and the main app
on the same computer. Stop them together with Ctrl-C, or run `make stop-local`
in another terminal. If you override `DATA_ROOT`, use the same value for both
commands. The SDR service requires
`rtl_adsb` and an RTL-SDR device. It listens on `127.0.0.1:8090` and connects
to the main app at `127.0.0.1:8080` by default.

For different settings, override Make variables, for example:

```sh
make run-local SDR_DEVICE=1 SDR_GAIN=40 SDR_PPM=0
```

If the main app's configured listen address uses a different port, set
`CALLBACK_URL` to the corresponding local URL. `SDR_LISTEN` and `SDR_URL` can
also be overridden together when changing the SDR service's address.

## Raspberry Pi

For initial setup on Ubuntu (including Ubuntu 26.04 on the Pi 400), run:

```sh
make initial-setup
```

This runs `make install-python`, `make install-go` and `make install-sdr` in
order. The Python and SDR steps use `sudo apt-get` and may ask for your sudo
password; the Go step installs for the current user. The SDR step installs
Ubuntu's `rtl-sdr` tools and its `librtlsdr0` dependency, including USB udev
rules. The package is in Ubuntu's Universe repository. These steps require
network access; Node.js is needed separately to build the main app's frontend.

After installing the SDR package, reconnect the dongle and run `rtl_test -t` to
check whether it is detected. Ubuntu's RTL-SDR USB rules use the `plugdev`
group; if access is denied, check `id -nG` and your device's permissions. If
needed, add your account with `sudo usermod -aG plugdev "$USER"`, then log out
and back in. A DVB-T kernel driver or another application may already be using
the device; investigate that if the test reports that the device is busy.
The SDR package supports RTL2832U receivers; other dongle types need their own
software.

Each install step can also be run separately: `make install-python`,
`make install-go`, or `make install-sdr`. When already running as root, pass
`SUDO=` to the Make command.

To install the latest stable Go release for the Pi or another Linux computer,
run `make install-go`. It detects the architecture, verifies the official
SHA-256 checksum and installs under `~/.local/trackflights-go` without sudo or
changing your shell configuration. The Makefile uses this installation when
present. To use it directly in your shell, run:

```sh
export PATH="$HOME/.local/trackflights-go/current/bin:$PATH"
```

Set `GO_INSTALL_DIR` to choose a different installation directory and use the
same value for later Make commands. The installer requires Python 3.12 or newer
and network access. It retains previous downloaded versions.

On a Raspberry Pi with Go 1.25 or newer and a supported Node.js installation, the ordinary
`make run`, `make run-sdr`, `make run-local`, `make stop-local`, and Nostr
commands use the Pi's native architecture. Run `make frontend-install` on the
Pi instead of copying `frontend/node_modules` from a different architecture.
The frontend build needs Node.js 20.19+ or 22.12+ and native build dependencies;
64-bit Raspberry Pi OS is recommended. `rtl_adsb` and a USB RTL-SDR device are
needed for reception. Node.js is not needed for the standalone SDR build.
`make test-sdr` tests that service without Node.js or hardware. `make test-pi`
runs Go and local-service tests using the frontend assets already present; it
does not rebuild or test the frontend.

To build on another computer for a 64-bit Pi and transfer the binaries:

```sh
make build-pi       # Both apps, in bin/linux-arm64/
make build-sdr-pi   # SDR app only; no frontend build needed
```

For 32-bit Raspberry Pi OS, use `make build-pi PI_ARCH=arm PI_ARM=7` (or
`PI_ARM=6` for an ARMv6 Pi). The output goes to `bin/linux-armv7/` or
`bin/linux-armv6/`. `GOARM` must match the Pi OS and CPU; `uname -m` on the Pi
helps identify them. Cross-compile the frontend on a supported host for older
32-bit Pis; the checked-in Node tooling may not run there.

Copy the matching output directory to the same location on the Pi. From the
project directory, `make run-pi`, `make run-sdr-pi`, or `make run-local-pi`
runs the prebuilt binaries without Go or Node.js. Pass the same `PI_ARCH` and
`PI_ARM` values used for a 32-bit build; stop the paired apps with
`make stop-local`. The SDR and Nostr variables used by the native run commands
also apply to these prebuilt commands.

The Nostr relay can run on the Pi or on a different computer. Docker selects the
native image architecture when pulling. If the published image does not support
your Pi, `make nostr-build-native-flight-data` builds the relay from its pinned
upstream source on that Pi. Then use `NOSTR_IMAGE=trackflights-nostr-relay:pi`
with the `nostr-*-flight-data` commands (and `NOSTR_ANALYSIS_IMAGE` for analysis). The container name must be unused or
already refer to that image; existing containers and data are never replaced.
Building the relay requires Docker and network access, and may take time on a Pi.

## Remote SDR receiver

Install `rtl_adsb` (from rtl-sdr) on the computer with the USB receiver. With
Go installed, start just the SDR service using:

```sh
make run-sdr SDR_LISTEN=0.0.0.0:8090
```

Press Ctrl-C to stop it. The `SDR_DEVICE`, `SDR_GAIN` and `SDR_PPM` Make
variables select the receiver settings. `run-sdr` defaults to listening on
`127.0.0.1:8090`; use a LAN address as above to accept connections from another
computer. Alternatively, build `go build -o bin/sdr ./cmd/sdr` for the receiver
(for example, set `GOOS=linux` and `GOARCH=arm64` when cross-compiling for a
64-bit Raspberry Pi) and run:

```sh
./bin/sdr -listen :8090 -device 0 -gain auto -ppm 0
```

Start the main application with the receiver's URL and the main application's
own URL as reachable from the receiver, for example:

```sh
TRACKFLIGHTS_SDR_URL=http://192.168.1.20:8090 \
TRACKFLIGHTS_CALLBACK_URL=http://192.168.1.10:8080 make run
```

Configure the main server's `server.listenAddress` to listen on a LAN interface
(e.g. `0.0.0.0:8080`) when the receiver is on another computer. The SDR service
keeps valid timestamped ADS-B frames in memory for 15 minutes. On subscription it
sends this history in order, then sends new frames and heartbeats. The main app
retries a lost subscription; the receiver stops the callback when the main app
stops responding. History is lost when the SDR service restarts. The main app
decodes the frames into aircraft positions using its configured receiver location.

The receiver exposes `GET /api/v1/history` and `POST /api/v1/subscription`
(`{"url":"http://main-host:8080/api/v1/sdr/frames"}`). These endpoints and the
main app's ingestion endpoint have no authentication; use them only on a trusted
network and restrict access with a firewall.

The live flight map reports the SDR signal status. “Signal detected” means the
receiver has decoded a valid ADS-B frame within the past 30 seconds. It also
shows when the receiver process is unavailable or the SDR service is
disconnected; a running receiver with no recent frames reports no signal.
The SDR endpoint `GET /api/v1/status` returns the current signal status.

## Nostr relays

Docker is required. The Makefile provides two independent relay presets:

| Purpose | Container | Local WebSocket URL | Data directory for a new container |
| --- | --- | --- | --- |
| ADS-B flight signals | `nostr-relay` | `ws://127.0.0.1:7447` | `data/nostr-relay` |
| Recording analysis results | `nostr-relay-analysis` | `ws://127.0.0.1:7448` | `data/nostr-relay-analysis` |

Start them independently with:

```sh
make nostr-start-flight-data
make nostr-start-analysis
```

The existing flight relay keeps its current Docker configuration and data mount;
it is not replaced or migrated. Each preset supports `nostr-pull-*`,
`nostr-start-*`, `nostr-stop-*`, `nostr-restart-*`, `nostr-status-*`,
`nostr-logs-*` and `nostr-check-*`, where `*` is `flight-data` or `analysis`.
For example, `make nostr-check-analysis` requests the analysis relay's NIP-11
information. Stopping a relay does not delete its data.

For a new flight relay, override `NOSTR_CONTAINER`, `NOSTR_BIND`, `NOSTR_PORT`
and `NOSTR_DATA_DIR` as needed. For the analysis relay, use
`NOSTR_ANALYSIS_CONTAINER`, `NOSTR_ANALYSIS_BIND`, `NOSTR_ANALYSIS_PORT` and
`NOSTR_ANALYSIS_DATA_DIR`. Give each relay a unique container name, port and
data directory. To run additional named relays, provide a distinct set of
these analysis variables consistently to each command, for example:

```sh
make nostr-start-analysis NOSTR_ANALYSIS_CONTAINER=nostr-relay-extra \
  NOSTR_ANALYSIS_PORT=7449 NOSTR_ANALYSIS_DATA_DIR="$PWD/data/nostr-relay-extra"
make nostr-status-analysis NOSTR_ANALYSIS_CONTAINER=nostr-relay-extra \
  NOSTR_ANALYSIS_PORT=7449 NOSTR_ANALYSIS_DATA_DIR="$PWD/data/nostr-relay-extra"
```

Both presets bind to localhost by default. `NOSTR_IMAGE` controls the flight
relay image and `NOSTR_ANALYSIS_IMAGE` controls the analysis image; the latter
defaults to `NOSTR_IMAGE`. Use `NOSTR_HTTP_URL` or `NOSTR_ANALYSIS_HTTP_URL` if
the corresponding health endpoint is reachable at another address. Pulling a
newer image does not replace an existing container or migrate its database.

### SDR signals on Nostr

The SDR app publishes valid received ADS-B frames to `ws://127.0.0.1:7447`
by default, using a dedicated private key created at `data/sdr/nostr.key` with
owner-only permissions. Keep this file to retain the publisher identity across
restarts. `make run-sdr` and `make run-local` use `NOSTR_KEY_FILE` under their
`DATA_ROOT`. The Make commands use the flight-data relay port `NOSTR_PORT`
(default `7447`), not the analysis relay's port. If the relay runs on another
computer, set `NOSTR_RELAY_HOST` to that computer's address, for example:

```sh
make run-sdr SDR_LISTEN=0.0.0.0:8090 NOSTR_RELAY_HOST=192.168.1.10
```

If the flight relay uses a different host port, pass the same `NOSTR_PORT` to
its start command and the SDR run command. The main app's relay URL is a
separate setting in Flight map → Map configuration and must match as well.
`NOSTR_RELAY` can still override the complete WebSocket URL, including `wss://`
for a remote TLS relay. For a Raspberry Pi, the relay address must be reachable
from the Pi; `127.0.0.1` refers to the Pi itself. Set `NOSTR_RELAY=` to disable publishing. When running the binary directly use
`-nostr-relay` and `-nostr-key-file`; an empty relay URL disables publishing.
The receiver does not require the relay to be running: it retries failed
publishes and holds up to 20,000 pending frames in memory. If the queue fills,
new frames are dropped from Nostr publishing and the count is logged. Pending
frames are lost when the SDR app exits. The main app's separate 15-minute
receiver buffer continues to operate.

Each signed Nostr event has kind `9090`, the indexed tag `["t",
"trackflights-sdr"]`, and `["v", "2"]`. Its `content` is a JSON string with
`type: "trackflights.sdr.signals"`, integer `version: 2`, and a `signals` array.
Each signal contains a UTC `received` timestamp, a CRC-validated 28-digit
uppercase hexadecimal ADS-B `frame`, and decoded `aircraft` data. Aircraft
always contains an ICAO address; callsign, altitude, velocity, heading, vertical
rate and position are included only when available. The decoder retains the most
recent data for each active aircraft, so fields may come from earlier frames.
The position timestamp records the time of the position fix, not necessarily
the time of the current frame. Position may be missing until a suitable pair of
CPR frames is received. Events contain up to 25 signals.

Subscribe with `{"kinds":[9090],"#t":["trackflights-sdr"]}` and inspect
the content's `version` before decoding. The SDR app serves
`GET /api/v1/schemas/flight-signals` to list versions and
`GET /api/v1/schemas/flight-signals/2` for the current JSON Schema of the
**parsed event content**. Example content is in
`internal/sdr/nostrpub/example-v2.json`. The schema endpoint belongs to the SDR
app, not the Nostr relay, and must be reachable by clients that need it.
Unknown versions return 404.
Nostr events are signed but not encrypted: relay operators and other authorized
relay users can read the signals.

### Live flights from Nostr

The main app subscribes to Nostr flight events by default at
`ws://127.0.0.1:7447`. In Flight map → Map configuration, change the Nostr
relay URL and optionally enter the SDR publisher's 64-character hexadecimal
public key to accept only its events. The SDR app logs its public key at startup.
Saving settings reconnects the subscriber; the live view shows its connection
status. The subscriber verifies event signatures, reads schema version 2,
and requests up to 500 recent events from the last 15 minutes when connecting.
Version 2 decoded positions are used when present. Version 1 events are ignored.

With Nostr reception enabled, flight frames from the direct SDR subscription are
not also applied, avoiding duplicate updates. Disable “Receive flights from
Nostr relay” to use the direct subscription instead. Direct SDR status remains
separate from the relay connection status. A relay URL and key must be configured
for the main app independently of the SDR publisher's `NOSTR_RELAY` setting.

## Blossom server

The Blossom server from the earlier tracking project can run on the machine
that runs the main app. Docker and network access for the initial image build
are required:

```sh
make blossom-start
make blossom-status
make blossom-check
make blossom-logs
make blossom-stop
```

The server listens at `http://127.0.0.1:3001` by default. Its first start copies
the example settings to `data/blossom/config.yml`; edit that file and restart
the server to change its settings. Uploads require authentication by default.
The default retention rules remove images after a month without access and
videos and other files after a week without access. Review the configuration
before relying on it for long-term storage.

Use **Open Blossom server** in the **Nostr** tab to open its web page. The
button's URL can be changed in the tab's tracking publication settings; use a
browser-reachable address if accessing the app from another computer. Start
the server with `make blossom-start` before opening it.

The Compose project uses its own persistent Docker volume. Stopping the server
does not remove its files, and this setup does not migrate or alter the old
server's data. Use `BLOSSOM_BIND`, `BLOSSOM_PORT`, `BLOSSOM_PROJECT`, and
`BLOSSOM_CONFIG` to override the bind address, host port, Compose project, and
configuration path. Pass the same overrides to subsequent commands. The
`blossom-check` command checks the landing page, which is enabled by default;
`BLOSSOM_HTTP_URL` overrides the URL it checks. `make blossom-build` explicitly
builds the image, and `make blossom-restart` restarts the container.

Object tracking can upload representative crops for qualifying tracks to Blossom.

## Object tracking

The Object tracking view uses the motion detection and event recording pipeline
from the earlier DWARF tracker. On Ubuntu with OpenCV 4.10 development
packages installed, run:

```sh
make install-tracking  # Ubuntu packages; requires sudo
make run-tracking      # Build the OpenCV worker and start the web app
```

To install FFmpeg by itself on Ubuntu, run `make install-ffmpeg`.

Alternatively, run `make build-tracking` once and start the web app with
`make run`. The worker uses GoCV 0.39 and the distribution's OpenCV 4.10; set
`OPENCV_PKG_CONFIG_LIBDIR` if the compatible OpenCV installation is elsewhere.
The worker is optional, so the flight server and Raspberry Pi SDR builds do not
require OpenCV. Set `TRACKFLIGHTS_TRACKING_WORKER` to an alternative worker
binary if needed. Build the worker on the machine that runs object tracking;
`make build-pi` cross-compiles only the main app and SDR service, not the
OpenCV worker.

Open **Object tracking** to choose a recorded video, upload an MP4, AVI, MOV or
MKV file (up to 512 MiB), or enter a live RTSP stream URL. Select the general
or rolling-ball profile, optionally save the original and mask videos, and
start tracking. Optional detection settings allow you to tune area, speed and
foreground thresholds. The view shows an annotated preview, progress and event files;
the stop button cancels the worker. Recorded events are stored in the configured
`tracking.outputDir` beneath the data root by default.

Open **Events** to browse recorded tracking events and their files. The same
event list is also available in **Object tracking**, and refreshes while a
session is running.

After a tracking job completes successfully, the main app publishes a signed
analysis note to `ws://127.0.0.1:7448` by default. As in the earlier tracker,
objects qualify when the straight-line distance between their first and last
positions is at least 800 pixels. Change the threshold in the **Nostr** tab;
zero includes all recorded tracks. The note includes the source name, event
summaries and first/last positions for qualifying tracks, with at most 100
events and 100 tracks per note, with further trimming to fit the relay's 16 KiB
content limit. It records when results have been limited or omitted.
It is a kind-1 Nostr event compatible with the earlier tracker's schema,
available at `/api/v1/tracking/nostr-schema`. The JSON in its content has type
`trackflights.tracking.analysis` and version `1`. Available object crops are
uploaded to Blossom and included as media links and `imeta` tags. The **Nostr**
tab can enable GIFs and set a public Blossom URL for other clients. Start the
local server with `make blossom-start`; if an upload fails, the note is still
published with a `mediaUnavailable` flag and the job reports the missing media.
Publishing errors appear in the tracking job status; the
recorded files remain available. Cancelled jobs do not publish a completion.

The app checks the analysis relay before publishing. If the configured relay is
local and auto-start is enabled, it runs `make nostr-start-analysis` when the
relay is unavailable. Docker, access to the Docker daemon, and the relay image
are required for auto-start. Use `make nostr-pull-analysis` to download the
image in advance. The app verifies that the relay responds before publishing.
The Nostr tab displays messages of all kinds from either configured relay,
showing up to 100 stored messages on connection and new messages as they arrive.
Its relay settings configure the ADS-B relay used by the main app and the
tracking-analysis relay separately; the status indicator applies to the relay
selected for the message display. Configure the SDR app separately to publish
to the ADS-B relay. The tab also offers tracking publication settings: enable/disable publishing, relay
URL, local auto-start and signing-key file. The key defaults to
`data/tracking/nostr.key` and is created with owner-only permissions. Relay
messages are signed but not encrypted, so everyone with access to the relay can
read the published analysis.

Select an event, then choose an object under **Tracked objects** to play its
saved close-ups and view its path. When video is available, the close-up and
path marker follow the same timeline as both videos, and either set of playback
controls can play, pause, or seek all views. Close-ups are sampled, so the
nearest saved frame is shown; gaps without a nearby image are left empty.
Selecting an object that is not visible at the current time moves playback to
its first detected position.
Events without video retain standalone object playback. Enter a name and choose **Save name**; names are stored per
event in `track_names.json` and are compatible with the earlier tracker. Names
for the bundled sample are stored separately beneath the app's data root.
New full-video sessions also save tracking metadata and sampled close-ups;
older sessions without those files cannot show object details.

When both recordings are available, **Events** shows the tracked and original
videos side by side. Shared play, pause and seek controls keep them together;
if one buffers, both wait. On narrow screens they are stacked. Select an object
above the videos and enable **Show selected object on original video** to overlay
only that object's box and path; the overlay follows the shared video timeline.
The original is otherwise unmarked. Full-video sessions
use their uploaded source as the original. **Save original video** is enabled
by default for new event clips; saving these files requires additional disk
space. For older events without an original clip, the app tries to recover
the corresponding frames from an uploaded source when the event uniquely
matches a full session. Events without a saved or uniquely matched original
show the tracked video alone. Original videos are converted to a cached
browser-compatible MP4 on first viewing and require FFmpeg and disk space.

Expand **Overlay appearance** above the videos to adjust the selected-object
box size, box line thickness and tracking line thickness. These settings are
saved in the app configuration and apply to the original-video overlay.

Tracked videos play in the Events view, with a download link for the original
file. Older AVI clips are converted to MP4 on first viewing with FFmpeg; this
can take time and uses additional disk space in `tracking-playback` beneath the
data root. The conversion is cached and the original AVI is preserved.

For a recorded video, leave **Save full tracked video (MP4)** selected when
starting tracking. When processing and encoding finish, select the `session-…`
entry in **Events** to play the entire video with track boxes, trails, labels
and speeds. This produces a video for each new session. Full export needs FFmpeg and
extra disk space for a temporary AVI and the final MP4, and it takes additional
encoding time. The video has no audio. Live streams still show annotated
previews and record individual events, rather than a full-session MP4.

`testdata/tracking/tele-sample.mp4` is a copied DWARF recording for trying the
pipeline; it may not produce a recorded event using the default thresholds.
`testdata/tracking/sample-event` contains a copied event summary, tracking
metadata and one object crop for reviewing the event interface. Its original
metadata references other files that are not included in the fixture. Run
`make test-tracking` to build the worker and run the migrated OpenCV tests,
including the sample-video and worker/API integration checks.
