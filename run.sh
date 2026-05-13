#!/usr/bin/env bash

cd "$( dirname "${BASH_SOURCE[0]}" )"

set -e

docker compose down
docker build -t nanopub/router .
docker compose up
