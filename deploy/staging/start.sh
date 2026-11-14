#!/bin/sh
set -eu

case "${POOLTUNE_STAGING_ROLE:?set POOLTUNE_STAGING_ROLE}" in
    detector)
        enrollment=$(mktemp)
        printf '%s\n' "${POOLTUNE_STAGING_ENROLLMENT:?set POOLTUNE_STAGING_ENROLLMENT}" > "$enrollment"
        exec pooltune-worker -target staging -enrollment "$enrollment" \
            -interval "${POOLTUNE_STAGING_INTERVAL:-1m}" -window "${POOLTUNE_STAGING_WINDOW:-5m}"
        ;;
    enrolled-consumer)
        enrollment=$(mktemp)
        printf '%s\n' "${POOLTUNE_STAGING_LOOKUP:?set POOLTUNE_STAGING_LOOKUP}" > "$enrollment"
        if [ -n "${POOLTUNE_STAGING_ROOKERY_CA:-}" ]; then
            POOLTUNE_LAB_ROOKERY_CA_FILE=$(mktemp)
            printf '%s\n' "$POOLTUNE_STAGING_ROOKERY_CA" > "$POOLTUNE_LAB_ROOKERY_CA_FILE"
            export POOLTUNE_LAB_ROOKERY_CA_FILE
        fi
        exec pooltune-consumer -target rookery-enrolled -project "${POOLTUNE_STAGING_PROJECT:?set POOLTUNE_STAGING_PROJECT}" \
            -enrolled-lookup "$enrollment"
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
