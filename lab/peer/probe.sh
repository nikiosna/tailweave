#!/bin/sh
# Connects back on request, so tests can open connections from a peer to the machine under
# test without Docker access (the macOS VM has none). Reads one line
# "<tcp|udp|ping> <IPv4> <port>" and prints what came back: the HTTP body for tcp, the
# datagram for udp, "ok" for a ping that was answered.
read -r kind ip port || exit 1
case $ip in "" | *[!0-9.]*) echo "bad address"; exit 1 ;; esac
case $port in "" | *[!0-9]*) echo "bad port"; exit 1 ;; esac
case $kind in
	tcp) wget -qO- -T3 "http://$ip:$port/" ;;
	udp) echo probe | socat -T3 - "UDP:$ip:$port" ;;
	ping) ping -c1 -W3 "$ip" >/dev/null && echo ok ;;
	*) echo "bad kind"; exit 1 ;;
esac
