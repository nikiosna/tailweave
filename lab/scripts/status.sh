#!/usr/bin/env bash
# Show nodes of every lab tailnet.
. "$(dirname "$0")/common.sh"
load_scenario

echo "scenario: ${SCENARIO:-none}"
for net in "${TAILNETS[@]}"; do
	echo
	echo "tailnet $net (${HS_URL[$net]}, base_domain $( [ $net = a ] && echo "$A_BASE_DOMAIN" || echo "$B_BASE_DOMAIN"))"
	hs "$net" nodes list -o json | jq -r '
		(["ID","NAME","IPS","ONLINE","ROUTES"] | @tsv),
		((. // [])[] | [.id, .given_name, (.ip_addresses | join(",")), .online,
		        ((.approved_routes // []) | join(","))] | @tsv)' | column -t -s $'\t'
done
