# service-request-router

JSON-driven reverse proxy router in Go. It forwards each incoming request to a configured service based on path matching rules.

## Features

- Route by `exact`, `prefix`, or `regex`
- Configurable `sort` priority per rule
- Deterministic tie-breaking to avoid broad prefixes winning accidentally
- No database dependencies (single JSON config only)

## Config format

Create a `config.json` file:

```json
{
  "port": 8080,
  "rules": [
    {
      "name": "core-v2",
      "prefix": "/core/v2",
      "host": "http://localhost:9002",
      "sort": 10
    },
    {
      "name": "core",
      "prefix": "/core",
      "host": "http://localhost:9001",
      "sort": 10
    },
    {
      "name": "health",
      "exact": "/health",
      "host": "http://localhost:9010",
      "sort": 20
    },
    {
      "name": "fallback-regex",
      "regex": "^/v[0-9]+/.*$",
      "host": "http://localhost:9020",
      "sort": 1
    }
  ]
}
```

### Rule fields

- `host` (required): target service base URL
- `sort` (optional, default `0`): higher value has higher priority
- `exact` | `prefix` | `regex`: exactly one is required
- `name` (optional): for readability

### Matching behavior

Rules are ordered by:

1. `sort` descending
2. Match type priority: `exact` > `prefix` > `regex`
3. Longer matcher string first (e.g., `/core/v2` before `/core`)
4. Original config order

This ensures `/core/v2/...` does not fall into `/core` when both are present.

## Run

```bash
go run . -config ./config.json
```

For a starter file, copy `config.example.json` to `config.json` and update hosts.
