#!/bin/sh
set -eu

app="/app/torboxarr"
config_dir="/config"

log() {
    printf '%s\n' "$*" >&2
}

fail() {
    log "entrypoint error: $*"
    exit 1
}

need_num() {
    case "$2" in
        ''|*[!0-9]*)
            fail "$1 must be a numeric UID/GID value"
            ;;
    esac
}

ensure_writable_dir() {
    dir="$1"

    if [ -d "$dir" ]; then
        [ -w "$dir" ] || fail "$dir is not writable by uid $(id -u). Use PUID/PGID startup or prepare the mount ownership manually."
        return
    fi

    mkdir -p "$dir" 2>/dev/null || fail "cannot create $dir as uid $(id -u). Use root startup with PUID/PGID or prepare the mount ownership manually."
    [ -w "$dir" ] || fail "$dir was created but is not writable by uid $(id -u)."
}

if [ ! -x "$app" ]; then
    fail "missing executable $app"
fi

if [ "$(id -u)" = "0" ]; then
    puid="${PUID:-}"
    pgid="${PGID:-}"

    [ -n "$puid" ] || fail "PUID is required when starting the container as root"
    [ -n "$pgid" ] || fail "PGID is required when starting the container as root"

    need_num "PUID" "$puid"
    need_num "PGID" "$pgid"

    mkdir -p "$config_dir"
    chown -R "$puid:$pgid" "$config_dir" || fail "failed to chown $config_dir to $puid:$pgid"

    exec su-exec "$puid:$pgid" "$app"
fi

ensure_writable_dir "$config_dir"
exec "$app"
