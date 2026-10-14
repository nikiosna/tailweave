#!/usr/bin/env bash
# Print the reusable preauth key and control URL for a tailnet, for logging in the DUT.
#
# usage: keys.sh <a|b>    prints: <login-server> <authkey>
. "$(dirname "$0")/common.sh"
load_scenario

net=${1:?usage: keys.sh <a|b>}
var="KEY_${net^^}"
[ -n "${!var}" ] || die "no key for tailnet $net, run bootstrap first"
echo "${HS_URL[$net]} ${!var}"
