#!/bin/sh
# Start the test services, then hand over to tailscale's containerboot.
set -eu

name="${TS_HOSTNAME:-$(hostname)}"
mkdir -p /srv/www
echo "$name" > /srv/www/index.html

# Addresses "behind" a subnet router, so routed traffic gets an answer from this peer.
for a in $(echo "${LAB_EXTRA_ADDRS:-}" | tr ',' ' '); do
	ip addr add "$a" dev lo
done

httpd -p 80 -h /srv/www
socat -T5 UDP-LISTEN:7,fork,reuseaddr EXEC:'/bin/cat' &
socat -T5 UDP-LISTEN:8,fork,reuseaddr SYSTEM:"echo $name" &
iperf3 -s -D
socat TCP-LISTEN:9,fork,reuseaddr EXEC:/usr/local/bin/lab-probe &

exec /usr/local/bin/containerboot
