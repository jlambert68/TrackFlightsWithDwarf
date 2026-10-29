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
