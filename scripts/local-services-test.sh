#!/usr/bin/env bash
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
test_dir=$(mktemp -d)
runner=
cleanup() {
  if [[ -n $runner ]]; then
    make --no-print-directory -C "$root" stop-local DATA_ROOT="$test_dir/data" >/dev/null 2>&1 || true
    kill "$runner" 2>/dev/null || true
    wait "$runner" 2>/dev/null || true
  fi
  rm -rf "$test_dir"
}
trap cleanup EXIT
mkdir -p "$test_dir/bin"
for name in sdr trackflights; do
  cat > "$test_dir/bin/$name" <<'STUB'
#!/usr/bin/env bash
name=${0##*/}
trap 'touch "$TEST_DIR/$name.stopped"; exit 0' TERM
touch "$TEST_DIR/$name.started"
while :; do sleep 0.1; done
STUB
  chmod +x "$test_dir/bin/$name"
done

TEST_DIR="$test_dir" make --no-print-directory -C "$root" -o build run-local BIN_DIR="$test_dir/bin" DATA_ROOT="$test_dir/data" >"$test_dir/start.log" 2>&1 &
runner=$!
for ((i=0; i<100; i++)); do
  if [[ -f $test_dir/sdr.started && -f $test_dir/trackflights.started ]]; then break; fi
  if ! kill -0 "$runner" 2>/dev/null; then cat "$test_dir/start.log" >&2; exit 1; fi
  sleep 0.1
done
[[ -f $test_dir/sdr.started && -f $test_dir/trackflights.started ]] || { echo 'Services failed to start' >&2; exit 1; }
make --no-print-directory -C "$root" stop-local DATA_ROOT="$test_dir/data"
wait "$runner" || true
runner=
[[ -f $test_dir/sdr.stopped && -f $test_dir/trackflights.stopped ]] || { echo 'Both services must receive shutdown' >&2; exit 1; }
[[ ! -e $test_dir/data/local-services.pid ]] || { echo 'Supervisor PID file was not removed' >&2; exit 1; }
output=$(make --no-print-directory -C "$root" stop-local DATA_ROOT="$test_dir/data")
[[ $output == *'not running'* ]] || { echo 'Stopping again should report no running services' >&2; exit 1; }
echo 'stop-local test passed'
