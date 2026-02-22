# Deterministic payload fixtures

- `envelope_v1_deterministic.cbor.b64` stores a deterministic EnvelopeV1 CBOR blob encoded as base64 text (to avoid committing binary artifacts).
- It encodes the following CBOR map (definite length):
  - `"v" => 1`
  - `"id" => "env-fixed-0001"`
  - `"payload" => h'00112233445566778899AABBCCDDEEFF'`

Generation command used:

```bash
python - <<'PY'
from pathlib import Path
hexstr='a36176016269646e656e762d66697865642d30303031677061796c6f61645000112233445566778899aabbccddeeff'
import base64
raw = bytes.fromhex(hexstr)
Path('tests/fixtures/envelope_v1_deterministic.cbor.b64').write_text(base64.b64encode(raw).decode() + '\n')
PY
```
