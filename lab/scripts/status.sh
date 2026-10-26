#!/usr/bin/env bash
# Show nodes of every lab tailnet.
. "$(dirname "$0")/common.sh"
load_scenario

echo "scenario: ${SCENARIO:-none}"
for net in "${TAILNETS[@]}"; do
	echo
	echo "tailnet $net (${HS_URL[$net]}, base_domain $( [ $net = a ] && echo "$A_BASE_DOMAIN" || echo "$B_BASE_DOMAIN"))"
	hs "$net" nodes list -o json | jq -r '
		(["ID","NAME","IPS","ONLINE","EXIT","ROUTES"] | @tsv),
		((. // [])[] | (.approved_routes // []) as $r | [.id, .given_name, (.ip_addresses | join(",")), .online,
		        (if any($r[]; . == "0.0.0.0/0") then "yes" else "-" end),
		        ($r | map(select(. != "0.0.0.0/0" and . != "::/0")) | join(","))] | @tsv)' | column -t -s $'\t'
done
