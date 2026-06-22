#!/usr/bin/env bash

set -euo pipefail

GCP_REGION=${GCP_REGION:-europe-central2}
MAX_AGE_HOURS=${MAX_AGE_HOURS:-8}
DRY_RUN=${DRY_RUN:-true}

cutoff_timestamp() {
    date -u -d "${MAX_AGE_HOURS} hours ago" +%Y-%m-%dT%H:%M:%SZ
}

run() {
    if [ "${DRY_RUN}" = "true" ]; then
        printf 'dry-run: %q' "$1"
        shift
        printf ' %q' "$@"
        printf '\n'
        return 0
    fi
    "$@"
}

delete_unused_disks() {
    for zone in "${GCP_REGION}-a" "${GCP_REGION}-b" "${GCP_REGION}-c"; do
        gcloud compute disks list \
            --zones="${zone}" \
            --format='value(name,users)' |
            while read -r disk users; do
                [ -n "${disk}" ] || continue
                [ -z "${users}" ] || continue
                run gcloud compute disks delete "${disk}" --zone="${zone}" --quiet
            done
    done
}

delete_old_clusters() {
    local cutoff
    cutoff=$(cutoff_timestamp)

    gcloud container clusters list \
        --filter="createTime<${cutoff}" \
        --format='value(name,location)' |
        while read -r name location; do
            [ -n "${name}" ] || continue
            run gcloud container clusters delete "${name}" --location="${location}" --quiet
        done
}

delete_old_firewalls() {
    local cutoff
    cutoff=$(cutoff_timestamp)

    gcloud compute firewall-rules list \
        --filter="creationTimestamp<${cutoff} AND NOT name~'^(default-|allow-all$|deny-all$)'" \
        --format='value(name)' |
        while read -r firewall; do
            [ -n "${firewall}" ] || continue
            run gcloud compute firewall-rules delete "${firewall}" --quiet
        done
}

delete_old_addresses() {
    local cutoff
    cutoff=$(cutoff_timestamp)

    gcloud compute addresses list \
        --filter="creationTimestamp<${cutoff} AND status=RESERVED" \
        --format='value(name,region)' |
        while read -r name region; do
            [ -n "${name}" ] || continue
            if [ -n "${region}" ]; then
                run gcloud compute addresses delete "${name}" --region="${region##*/}" --quiet
            else
                run gcloud compute addresses delete "${name}" --global --quiet
            fi
        done
}

delete_old_forwarding_rules() {
    local cutoff
    cutoff=$(cutoff_timestamp)

    gcloud compute forwarding-rules list \
        --filter="creationTimestamp<${cutoff}" \
        --format='value(name,region)' |
        while read -r name region; do
            [ -n "${name}" ] || continue
            if [ -n "${region}" ]; then
                run gcloud compute forwarding-rules delete "${name}" --region="${region##*/}" --quiet
            else
                run gcloud compute forwarding-rules delete "${name}" --global --quiet
            fi
        done
}

main() {
    date -u +'%Y-%m-%dT%H:%M:%SZ'
    printf 'GCP_REGION=%s MAX_AGE_HOURS=%s DRY_RUN=%s\n' "${GCP_REGION}" "${MAX_AGE_HOURS}" "${DRY_RUN}"
    delete_unused_disks
    delete_old_clusters
    delete_old_firewalls
    delete_old_addresses
    delete_old_forwarding_rules
}

main "$@"
