# Aethos client-relay v1 conformance fixtures

These fixtures are vendored from canonical protocol docs in `aethos` and used by
`tests/protocol_conformance_fixtures_test.go`.

Upstream currently has no machine-readable fixture pack. These files were derived
from Markdown examples and protocol text in `SOURCE.txt`.

## Layout

- `frames/client_to_relay/*.json`: client frame templates
- `frames/relay_to_client/*.json`: relay frame templates (expected subsets)
- `payloads/*.txt`: payload fixtures (base64url text)
- `cases/*.json`: executable fixture cases
- `SOURCE.txt`: provenance to upstream `aethos`

## Canonical vs transitional coverage

- `expectation_mode: "canonical_v1"` cases assert currently aligned canonical-v1 behavior.
- `expectation_mode: "transitional_compat"` cases assert intentional relay divergences during migration.
- Transitional cases must declare `known_divergences` and should assert those divergences explicitly
  (for example via `forbidden_fields`, `required_fields`, and `field_equals`) instead of silently
  changing canonical expectations.

## Case schema (`cases/*.json`)

```json
{
  "name": "string",
  "expectation_mode": "canonical_v1|transitional_compat",
  "known_divergences": ["required when expectation_mode=transitional_compat"],
  "description": "string",
  "relay": {
    "ack_driven_suppression": false
  },
  "clients": [
    {
      "id": "sender",
      "wayfarer_id": "<64-char lowercase hex>",
      "device_id": "optional"
    }
  ],
  "steps": [
    {
      "op": "dial|close|hello|send|pull|ack|expect_frame|expect_messages|assert_delivery_state",
      "client": "client-id",
      "frame_ref": "frames/...json",
      "to_client": "client-id",
      "payload_ref": "payloads/...txt",
      "ttl_seconds": 120,
      "limit": 10,
      "msg_id_var": "captured-var-name",
      "capture_payload_var": "captured-var-name",
      "required_fields": ["msg_id"],
      "forbidden_fields": ["relay_id"],
      "field_equals": {"from": "$sender.wayfarer_id"},
      "equal_fields": [["at", "received_at"]],
      "capture": {"send_msg_id": "msg_id"},
      "count": 1,
      "message_index": 0,
      "message_required_fields": ["msg_id", "from", "payload_b64", "received_at"],
      "message_forbidden_fields": ["expires_at"],
      "message_field_equals": {"msg_id": "$send_msg_id"},
      "message_equal_fields": [["msg_id", "$send_msg_id"]],
      "message_payload_equals": "$sent_payload",
      "recipient": "$recipient.delivery_identity",
      "delivered": true
    }
  ]
}
```

Token resolution supports:

- `$<capture-var>`
- `$<client-id>.wayfarer_id`
- `$<client-id>.device_id`
- `$<client-id>.delivery_identity`
