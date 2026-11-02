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
