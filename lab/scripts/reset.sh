#!/usr/bin/env bash
# Tear the lab down completely (containers, volumes, state). Use bootstrap.sh to start again.
. "$(dirname "$0")/common.sh"
load_scenario

COMPOSE_PROFILES=subnets,exit-nodes compose down --volumes --remove-orphans
"$(dirname "$0")/netns-playground.sh" down || true
rm -rf "$STATE_DIR"
log "lab reset"
