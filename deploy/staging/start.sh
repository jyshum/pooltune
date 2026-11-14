#!/bin/sh
set -eu

case "${POOLTUNE_STAGING_ROLE:?set POOLTUNE_STAGING_ROLE}" in
    detector)
        enrollment=$(mktemp)
        printf '%s\n' "${POOLTUNE_STAGING_ENROLLMENT:?set POOLTUNE_STAGING_ENROLLMENT}" > "$enrollment"
        exec pooltune-worker -target staging -enrollment "$enrollment" \
            -interval "${POOLTUNE_STAGING_INTERVAL:-1m}" -window "${POOLTUNE_STAGING_WINDOW:-5m}"
        ;;
    consumer)
        exec pooltune-consumer -target staging -project "${POOLTUNE_STAGING_PROJECT:?set POOLTUNE_STAGING_PROJECT}" \
            -fixture "${POOLTUNE_STAGING_FIXTURE:?set POOLTUNE_STAGING_FIXTURE}" -mode scripted
        ;;
    *)
        printf '%s\n' 'unknown staging service role' >&2
        exit 2
        ;;
esac
