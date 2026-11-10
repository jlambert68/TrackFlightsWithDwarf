#!/usr/bin/env bash
set -euo pipefail
: "${DOCKER:=docker}" "${NOSTR_CONTAINER:=nostr-relay}" "${NOSTR_IMAGE:=ghcr.io/mattn/nostr-relay:latest}"
: "${NOSTR_BIND:=127.0.0.1}" "${NOSTR_PORT:=7447}" "${NOSTR_DATA_DIR:=$PWD/data/nostr-relay}"
: "${NOSTR_HTTP_URL:=http://127.0.0.1:$NOSTR_PORT}"

# Distinguish an absent container from a daemon that cannot be reached.
"$DOCKER" info >/dev/null
exists() { "$DOCKER" container inspect "$NOSTR_CONTAINER" >/dev/null 2>&1; }
verify() {
  local image
  image=$("$DOCKER" inspect --format '{{.Config.Image}}' "$NOSTR_CONTAINER")
  if [[ $image != "$NOSTR_IMAGE" ]]; then
    printf 'Container %s uses image %s, not %s; refusing to change it.\n' "$NOSTR_CONTAINER" "$image" "$NOSTR_IMAGE" >&2
    exit 1
  fi
}
case "${1:-}" in
  pull) "$DOCKER" pull "$NOSTR_IMAGE" ;;
  start)
    if exists; then
      verify
      if [[ $("$DOCKER" inspect --format '{{.State.Running}}' "$NOSTR_CONTAINER") == true ]]; then
        echo "Relay $NOSTR_CONTAINER is already running."
      else
        "$DOCKER" start "$NOSTR_CONTAINER"
      fi
    else
      mkdir -p "$NOSTR_DATA_DIR"
      data_dir=$(cd "$NOSTR_DATA_DIR" && pwd -P)
      "$DOCKER" run -d --name "$NOSTR_CONTAINER" --restart unless-stopped \
        -p "$NOSTR_BIND:$NOSTR_PORT:7447" -e DATABASE_URL=/data/nostr-relay.sqlite \
        -e LOG_LEVEL=info --mount "type=bind,source=$data_dir,target=/data" "$NOSTR_IMAGE"
    fi ;;
  stop|restart)
    if ! exists; then echo "Relay $NOSTR_CONTAINER does not exist." >&2; exit 1; fi
    verify
    "$DOCKER" "$1" "$NOSTR_CONTAINER" ;;
  status)
    if ! exists; then echo "Relay $NOSTR_CONTAINER does not exist."; exit 1; fi
    verify
    "$DOCKER" inspect --format 'Name: {{.Name}}{{"\n"}}Image: {{.Config.Image}}{{"\n"}}Status: {{.State.Status}}{{"\n"}}Ports: {{json .NetworkSettings.Ports}}{{"\n"}}Mounts: {{json .Mounts}}' "$NOSTR_CONTAINER" ;;
  logs)
    if ! exists; then echo "Relay $NOSTR_CONTAINER does not exist." >&2; exit 1; fi
    verify
    "$DOCKER" logs --tail 100 "$NOSTR_CONTAINER" ;;
  check)
    if ! exists; then echo "Relay $NOSTR_CONTAINER does not exist." >&2; exit 1; fi
    verify
    if [[ $("$DOCKER" inspect --format '{{.State.Running}}' "$NOSTR_CONTAINER") != true ]]; then echo 'Relay is not running.' >&2; exit 1; fi
    curl --fail --silent --show-error --max-time 5 -H 'Accept: application/nostr+json' "$NOSTR_HTTP_URL/"
    printf '\n' ;;
  *) echo 'Usage: nostr-relay.sh {pull|start|stop|restart|status|logs|check}' >&2; exit 2 ;;
esac
