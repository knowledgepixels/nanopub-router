# Nanopub Router

A tiny HTTP redirector for the nanopublication network. Requests to a known
service-type prefix are answered with `307 Temporary Redirect` pointing at a
healthy instance of that service.

It is **not** a reverse proxy: it never relays the request body. The chosen
instance handles the actual request directly, so the router stays stateless
and cheap.

## How it picks a target

The router does not scan the network itself. It polls a
[Nanopub Monitor](https://github.com/knowledgepixels/nanopub-monitor)'s
`/.json` feed (default: `monitor.knowledgepixels.com` with `monitor.petapico.org`
as fallback) and keeps a last-known-good snapshot. For each request it filters
to:

- `status == "OK"`
- not a test instance (unless `--include-test`)
- for `nanopub-registry`: `hashGroup == "consensus"` (the majority scope hash
  among registries sharing the same setting), unless `--require-consensus=false`

Then picks one uniformly at random. Random was chosen over fastest/round-robin
because it spreads load with zero in-memory state and is robust to occasional
slow responders. Swap `pick()` if you want a different policy.

## Routes

| Path | Effect |
| --- | --- |
| `/nanopub-registry/...` | 307 to a healthy `nanopub-registry` instance |
| `/nanopub-query/...` | 307 to a healthy `nanopub-query` instance |
| `/healthz` | 200 iff snapshot is fresh |
| `/status.json` | Snapshot summary and per-prefix candidate counts |
| `/` | Plain-text help page |

The path portion after the prefix and any query string are appended to the
chosen instance's base URL.

## Configuration

All flags also work as `ROUTER_*` env vars (e.g. `ROUTER_POLL_INTERVAL=2m`).

| Flag | Default | Meaning |
| --- | --- | --- |
| `-addr` | `:8080` | Listen address |
| `-monitors` | `https://monitor.knowledgepixels.com/.json,https://monitor.petapico.org/.json` | Monitor JSON URLs, tried in order |
| `-poll` | `60s` | Poll interval |
| `-max-age` | `30m` | Snapshot considered stale beyond this; `/healthz` fails and routing returns 503 |
| `-include-test` | `false` | Allow test instances as candidates |
| `-require-consensus` | `true` | Require `hashGroup=consensus` for registry candidates |
| `-http-timeout` | `10s` | HTTP timeout for monitor polls |

## Run

```bash
go run .                                    # local
go test ./...                               # unit tests
docker build -t nanopub-router .            # image
docker run --rm -p 8080:8080 nanopub-router # served on :8080
```

Or with Docker Compose (host port `7899`):

```bash
./run.sh                                    # docker compose down/build/up
```

Then:

```bash
curl -i http://localhost:8080/nanopub-registry/    # plain docker run
curl -i http://localhost:7899/nanopub-registry/    # docker compose
curl    http://localhost:7899/status.json
```

## License

MIT
