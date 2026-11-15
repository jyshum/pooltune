#!/bin/sh
set -eu

case "${POOLTUNE_STAGING_ROLE:?set POOLTUNE_STAGING_ROLE}" in
    enrolled-detector|enrolled-consumer|measured-enrolled-consumer)
        if [ -n "${POOLTUNE_STAGING_ROOKERY_CA:-}" ]; then
            POOLTUNE_LAB_ROOKERY_CA_FILE=$(mktemp)
            printf '%s\n' "$POOLTUNE_STAGING_ROOKERY_CA" > "$POOLTUNE_LAB_ROOKERY_CA_FILE"
            export POOLTUNE_LAB_ROOKERY_CA_FILE
        fi
        ;;
esac

case "${POOLTUNE_STAGING_ROLE:?set POOLTUNE_STAGING_ROLE}" in
    web-preview)
        set -- -interval "${POOLTUNE_STAGING_INTERVAL:-1m}" \
            -window "${POOLTUNE_STAGING_WINDOW:-5m}" -queue-interval "${POOLTUNE_STAGING_QUEUE_INTERVAL:-2s}"
        if [ "${POOLTUNE_OWNED_CRON_CAPTURE:-0}" = 1 ]; then
            if [ "${POOLTUNE_STAGING_PRIVATE_STATE:-0}" != 1 ] || [ -z "${POOLTUNE_OWNED_CRON_FIXTURE:-}" ]; then
                printf '%s\n' 'owned Cron capture requires private staging and an exact fixture' >&2
                exit 2
            fi
            node /app/investigator/owned-cron-traffic.ts &
            capture=$!
            pooltune-web "$@" &
            worker=$!
            trap 'kill "$capture" "$worker" 2>/dev/null || true' INT TERM
            status=0
            wait "$worker" || status=$?
            kill "$capture" 2>/dev/null || true
            wait "$capture" 2>/dev/null || true
            exit "$status"
        fi
        exec pooltune-web "$@"
        ;;
    project-monitor)
        case "${POOLTUNE_PROJECT_MODE:-standby}" in
            standby)
                printf '%s\n' '{"event":"standby"}'
                exec sleep infinity
                ;;
            disconnect)
                pooltune-project disconnect -ref "${POOLTUNE_PROJECT_REF:?}" -key "${POOLTUNE_PROJECT_KEY:?}"
                exec sleep infinity
                ;;
            active)
                POOLTUNE_PROJECT_CA_FILE=$(mktemp)
                printf '%s\n' "${POOLTUNE_PROJECT_CA:?}" > "$POOLTUNE_PROJECT_CA_FILE"
                export POOLTUNE_PROJECT_CA_FILE
                exec pooltune-project serve -ref "${POOLTUNE_PROJECT_REF:?}" -key "${POOLTUNE_PROJECT_KEY:?}" \
                    -interval "${POOLTUNE_STAGING_INTERVAL:-1m}" -window "${POOLTUNE_STAGING_WINDOW:-5m}" \
                    -queue-interval "${POOLTUNE_STAGING_QUEUE_INTERVAL:-2s}"
                ;;
            *)
                printf '%s\n' 'unknown project monitor mode' >&2
                exit 2
                ;;
        esac
        ;;
    detector|enrolled-detector)
        target=staging
        if [ "$POOLTUNE_STAGING_ROLE" = enrolled-detector ]; then
            target=rookery-enrolled
        fi
        enrollment=$(mktemp)
        printf '%s\n' "${POOLTUNE_STAGING_ENROLLMENT:?set POOLTUNE_STAGING_ENROLLMENT}" > "$enrollment"
        exec pooltune-worker -target "$target" -enrollment "$enrollment" \
            -interval "${POOLTUNE_STAGING_INTERVAL:-1m}" -window "${POOLTUNE_STAGING_WINDOW:-5m}"
        ;;
    enrolled-consumer|measured-enrolled-consumer)
        enrollment=$(mktemp)
        printf '%s\n' "${POOLTUNE_STAGING_LOOKUP:?set POOLTUNE_STAGING_LOOKUP}" > "$enrollment"
        if [ "$POOLTUNE_STAGING_ROLE" = measured-enrolled-consumer ]; then
            set -- -measure-staging-lookup
        else
            set --
        fi
        exec pooltune-consumer -target rookery-enrolled -project "${POOLTUNE_STAGING_PROJECT:?set POOLTUNE_STAGING_PROJECT}" \
            -enrolled-lookup "$enrollment" "$@"
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
