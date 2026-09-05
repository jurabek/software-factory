# Software Factory API usage

Start the API and embedded UI with `go run main.go`. State defaults to `~/.software-factory`; override it with `SOFTWARE_FACTORY_DIR`. The API is loopback-only and has no CORS support.

Fetch the per-process mutation token:

```bash
TOKEN=$(curl -s http://127.0.0.1:8080/api/v1/control | jq -r .token)
```

Create and start a local draft:

```bash
CAMPAIGN_ID=$(curl -s -X POST http://127.0.0.1:8080/api/v1/campaigns \
  -H "X-Software-Factory-Token: $TOKEN" -H 'Content-Type: application/json' \
  -d '{"request":"Implement feature X","repository":{"type":"local","path":"/absolute/repository"}}' | jq -r .id)
curl -s -X POST "http://127.0.0.1:8080/api/v1/campaigns/$CAMPAIGN_ID/start" -H "X-Software-Factory-Token: $TOKEN"
```

For GitHub, use `{"type":"github","repo":"owner/repository"}`. Repository access starts only after the Start request.

Read routes include campaigns, phases, events, results, checks, and diff. Live events use `/api/v1/campaigns/{id}/events/stream`; reconnect with `Last-Event-ID` or `?after=`. Mutation routes are `start`, `approve`, `pause`, `resume`, `abort`, and `feedback`; inactive Campaigns support `DELETE`.

Planner results always include `questions`. If questions remain, answer them while awaiting approval:

```bash
curl -s -X POST "http://127.0.0.1:8080/api/v1/campaigns/$CAMPAIGN_ID/feedback" \
  -H "X-Software-Factory-Token: $TOKEN" -H 'Content-Type: application/json' \
  -d '{"feedback":"Use PostgreSQL and retain the public API.","current_plan_digest":"DIGEST_FROM_CAMPAIGN"}'
```

After the revised plan has an empty `questions` array, approve it:

```bash
curl -s -X POST "http://127.0.0.1:8080/api/v1/campaigns/$CAMPAIGN_ID/approve" -H "X-Software-Factory-Token: $TOKEN"
```

The Planner revision reuses its Campaign session and approval binds to the latest digest.

Campaign state and normalized events are stored in SQLite WAL and mirrored to campaign JSONL traces. Prompt audit copies and Pi sessions remain private beneath the factory directory. Do not expose that directory with a static server.

`curl` examples demonstrate the HTTP API; curl is not a Software Factory CLI.
