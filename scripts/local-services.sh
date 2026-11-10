#!/usr/bin/env bash
set -u

# The lock remains open for the lifetime of the supervisor, but not its children.
mkdir -p "$DATA_ROOT"
exec 9>"$DATA_ROOT/local-services.lock"
pidfile="$DATA_ROOT/local-services.pid"
script_path=$(realpath "$0")

if [[ ${1:-} == stop ]]; then
  if flock -n 9; then
    echo 'Local services are not running.'
    exit 0
  fi
  if [[ ! -r "$pidfile" ]]; then
    echo 'Local services are starting; try again shortly.' >&2
    exit 1
  fi
  read -r pid < "$pidfile"
  if [[ ! $pid =~ ^[0-9]+$ ]] || [[ ! -r /proc/$pid/cmdline ]] || ! tr '\0' '\n' < "/proc/$pid/cmdline" | grep -Fxq -- "$script_path"; then
    echo 'Cannot identify the local services supervisor; no signal sent.' >&2
    exit 1
  fi
  if ! kill -TERM "$pid" 2>/dev/null; then
    echo 'Could not stop the local services supervisor.' >&2
    exit 1
  fi
  if ! flock -w 20 9; then
    echo 'Local services did not stop within 20 seconds.' >&2
    exit 1
  fi
  echo 'Local services stopped.'
  exit 0
fi

if ! flock -n 9; then
  echo 'Local services are already running for this data directory.' >&2
  exit 1
fi
printf '%s\n' "$$" > "$pidfile"
sdr_pid= app_pid=
cleanup() {
  trap - EXIT INT TERM
  [[ -z $sdr_pid ]] || kill "$sdr_pid" 2>/dev/null || true
  [[ -z $app_pid ]] || kill "$app_pid" 2>/dev/null || true
  [[ -z $sdr_pid ]] || wait "$sdr_pid" 2>/dev/null || true
  [[ -z $app_pid ]] || wait "$app_pid" 2>/dev/null || true
  rm -f "$pidfile"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
"$BIN_DIR/sdr" -listen "$SDR_LISTEN" -device "$SDR_DEVICE" -gain "$SDR_GAIN" -ppm "$SDR_PPM" -nostr-relay "$NOSTR_RELAY" -nostr-key-file "$NOSTR_KEY_FILE" 9>&- & sdr_pid=$!
TRACKFLIGHTS_DATA_ROOT="$DATA_ROOT" TRACKFLIGHTS_SDR_URL="$SDR_URL" TRACKFLIGHTS_CALLBACK_URL="$CALLBACK_URL" "$BIN_DIR/trackflights" 9>&- & app_pid=$!
wait -n "$sdr_pid" "$app_pid"
