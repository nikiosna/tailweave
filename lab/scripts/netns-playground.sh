#!/usr/bin/env bash
# Manual experiment without tailweave: one tailscaled per tailnet, each in its own
# network namespace, both logged into their lab headscale. Shows that the stacks
# are isolated even though both tailnets hand out the same 100.64.0.x addresses.
#
# usage: netns-playground.sh up|down|status
#        netns-playground.sh exec <a|b> <cmd...>    run a command inside tw-<net>
#        netns-playground.sh ts <a|b> <args...>     tailscale CLI of that instance
. "$(dirname "$0")/common.sh"

RUN_DIR=/run/tailweave-lab
VAR_DIR=/var/lib/tailweave-lab
SUDO=$([ "$(id -u)" = 0 ] || echo sudo)

idx() { case $1 in a) echo 1 ;; b) echo 2 ;; *) die "unknown tailnet $1" ;; esac; }

ts() { local net=$1; shift; $SUDO tailscale --socket="$RUN_DIR/$net.sock" "$@"; }

up_ns() {
	local net=$1 i; i=$(idx "$net")
	local ns=tw-$net host=tw-$net-h peer=tw-$net-n
	if ! $SUDO ip netns list | grep -qw "$ns"; then
		$SUDO ip netns add "$ns"
		$SUDO ip link add "$host" type veth peer name "$peer"
		$SUDO ip link set "$peer" netns "$ns"
		$SUDO ip addr add "10.200.$i.1/30" dev "$host"
		$SUDO ip link set "$host" up
		$SUDO ip -n "$ns" addr add "10.200.$i.2/30" dev "$peer"
		$SUDO ip -n "$ns" link set "$peer" up
		$SUDO ip -n "$ns" link set lo up
		$SUDO ip -n "$ns" route add default via "10.200.$i.1"
		$SUDO mkdir -p "/etc/netns/$ns"
		echo "nameserver 1.1.1.1" | $SUDO tee "/etc/netns/$ns/resolv.conf" >/dev/null
	fi
	$SUDO mkdir -p "$RUN_DIR" "$VAR_DIR/$net"
	$SUDO test -f "$VAR_DIR/$net/tailscaled.state" || forget_dut "$net"
	if ! { [ -f "$RUN_DIR/$net.pid" ] && $SUDO kill -0 "$(cat "$RUN_DIR/$net.pid")" 2>/dev/null; }; then
		$SUDO sh -c "ip netns exec $ns tailscaled --tun=ts-$net --statedir=$VAR_DIR/$net \
			--socket=$RUN_DIR/$net.sock --port=$((41640 + i)) >$VAR_DIR/$net/tailscaled.log 2>&1 &
			echo \$! > $RUN_DIR/$net.pid"
		wait_for 20 "tailscaled $net" test -S "$RUN_DIR/$net.sock"
	fi
	local url key
	read -r url key < <("$(dirname "$0")/keys.sh" "$net")
	# Pass the key via file so it does not show up in the process list.
	printf '%s' "$key" | $SUDO tee "$VAR_DIR/$net/authkey" >/dev/null
	ts "$net" up --login-server="$url" --auth-key="file:$VAR_DIR/$net/authkey" --hostname="dut-$net" \
		--accept-dns=false --accept-routes=false --reset
	log "tw-$net: $(ts "$net" ip -4)"
}

nat_rules() { # nat_rules -A|-D
	$SUDO iptables -t nat "$1" POSTROUTING -s 10.200.0.0/16 ! -o tw-+ -j MASQUERADE
	# Docker sets FORWARD policy DROP; let the namespaces reach the lab bridge and the internet.
	$SUDO iptables "$1" DOCKER-USER -i tw-+ -j ACCEPT
	$SUDO iptables "$1" DOCKER-USER -o tw-+ -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT
}

case ${1:-} in
up)
	$SUDO iptables -t nat -C POSTROUTING -s 10.200.0.0/16 ! -o tw-+ -j MASQUERADE 2>/dev/null || nat_rules -I
	for net in "${TAILNETS[@]}"; do up_ns "$net"; done
	"$0" status
	;;
down)
	for net in "${TAILNETS[@]}"; do
		[ -f "$RUN_DIR/$net.pid" ] && $SUDO kill "$(cat "$RUN_DIR/$net.pid")" 2>/dev/null || true
		# Also catch instances whose pid file got lost, e.g. after an interrupted run.
		$SUDO pkill -f "^tailscaled .*--socket=$RUN_DIR/$net.sock" || true
		$SUDO ip netns del "tw-$net" 2>/dev/null || true
		$SUDO ip link del "tw-$net-h" 2>/dev/null || true
		$SUDO rm -rf "/etc/netns/tw-$net" "$VAR_DIR/$net" "$RUN_DIR/$net.pid"
		forget_dut "$net" || true
	done
	while $SUDO iptables -t nat -C POSTROUTING -s 10.200.0.0/16 ! -o tw-+ -j MASQUERADE 2>/dev/null; do
		nat_rules -D
	done
	;;
status)
	for net in "${TAILNETS[@]}"; do
		echo "--- tw-$net"
		ts "$net" status 2>&1 || true
	done
	;;
exec)
	net=${2:?net}; shift 2
	$SUDO ip netns exec "tw-$net" "$@"
	;;
ts)
	net=${2:?net}; shift 2
	ts "$net" "$@"
	;;
*)
	sed -n '2,9p' "$0" | sed 's/^# \{0,1\}//'
	exit 2
	;;
esac
