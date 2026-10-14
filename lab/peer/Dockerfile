# Lab peer: a tailscale node that also runs simple test services
# (HTTP over TCP, UDP echo, iperf3) so connectivity beyond TCP can be checked.
ARG TAILSCALE_VERSION=v1.102.5
FROM tailscale/tailscale:${TAILSCALE_VERSION}

RUN apk add --no-cache iperf3 socat bind-tools busybox-extras

COPY entrypoint.sh /usr/local/bin/lab-peer
ENTRYPOINT ["/usr/local/bin/lab-peer"]
