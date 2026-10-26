#!/usr/bin/env bash
# Bring the lab up for a scenario: headscales, users, preauth keys, peers, route approval.
#
# usage: bootstrap.sh [base|subnets|exit-nodes|dns-suffix|all]
. "$(dirname "$0")/common.sh"

scenario=${1:-base}
profiles="" a_dom=a.lab b_dom=b.lab
case $scenario in
	base) ;;
	subnets) profiles=subnets ;;
	exit-nodes) profiles=exit-nodes ;;
	dns-suffix) b_dom=a.lab ;;
	all) profiles=subnets,exit-nodes ;;
	*) die "unknown scenario: $scenario" ;;
esac
cat > "$STATE_DIR/scenario.env" <<EOT
SCENARIO=$scenario
COMPOSE_PROFILES=$profiles
A_BASE_DOMAIN=$a_dom
B_BASE_DOMAIN=$b_dom
EOT
load_scenario

log "scenario: $scenario"
compose up -d --wait headscale-a headscale-b
for net in "${TAILNETS[@]}"; do
	wait_for 60 "headscale-$net" hs "$net" users list
done

: > "$STATE_DIR/keys.env"
for net in "${TAILNETS[@]}"; do
	if ! hs "$net" users list -o json | jq -e --arg u "$LAB_USER" 'any((. // [])[]; .name == $u)' >/dev/null; then
		hs "$net" users create "$LAB_USER" >/dev/null
	fi
	uid=$(hs "$net" users list -o json | jq -r --arg u "$LAB_USER" '.[] | select(.name == $u) | .id')
	key=$(hs "$net" preauthkeys create --user "$uid" --reusable --expiration 720h -o json | jq -r .key)
	[ -n "$key" ] && [ "$key" != null ] || die "could not create preauth key for $net"
	echo "KEY_${net^^}=$key" >> "$STATE_DIR/keys.env"
	# For the REST API, which lab tests use, also from machines without Docker.
	apikey=$(hs "$net" apikeys create --expiration 720h)
	[ -n "$apikey" ] || die "could not create API key for $net"
	echo "APIKEY_${net^^}=$apikey" >> "$STATE_DIR/keys.env"
done
chmod 600 "$STATE_DIR/keys.env"
load_scenario

log "starting peers"
compose build
# One at a time, so IP allocation is deterministic: peer-a1 and peer-b1 both get 100.64.0.1.
# peer-b3 comes last: B then has one more node than A, so its IP is free in A.
enabled=" $(compose config --services | tr '\n' ' ') "
for p in peer-a1 peer-b1 peer-a2 peer-b2 router-a router-b exit-a exit-b peer-b3; do
	[[ $enabled == *" $p "* ]] || continue
	compose up -d --no-deps "$p"
	wait_for 90 "$p to come online" \
		sh -c "docker exec $p tailscale status --json | jq -e '.BackendState == \"Running\"'"
done

# The exit nodes' internet; it needs no tailnet.
[[ $enabled == *" inet "* ]] && compose up -d --no-deps inet

# Approve every advertised route so subnet routers and exit nodes are active.
for net in "${TAILNETS[@]}"; do
	hs "$net" nodes list -o json | jq -r '(. // [])[] | select((.available_routes // []) | length > 0)
		| "\(.id) \(.available_routes | join(","))"' |
	while read -r id routes; do
		log "approving routes on $net node $id: $routes"
		hs "$net" nodes approve-routes --identifier "$id" --routes "$routes" >/dev/null
	done
done

"$(dirname "$0")/status.sh"
