# Real local model verification

On **2026-10-20**, Atlas successfully ran genuine model inference through both
**Pi 1.3.0** and **OpenCode 2.1.0**, using a temporary localhost llama.cpp server.
This check used actual Qwen model weights and CPU generation. It did not use the
deterministic HTTP response fixture or any external provider credentials.

## Results

| Check through a freshly built Atlas CLI | Pi | OpenCode |
| --- | --- | --- |
| `init --template researcher` | Passed | Passed |
| Native model catalog lists configured local model | Passed | Passed |
| Background run, wait, streamed logs and durable identity | Passed | Passed |
| Generate requested codeword `NEBULA7319` | Passed | Passed |
| Close native process, resume, recall codeword without repeating it in prompt | Passed | Passed |
| Same native persistence handle after resume | Passed | Passed |
| Model selects native file-read tool and returns previously unseen file contents | Passed in fresh conversation | Passed |
| Native read approval surfaced and allowed through Atlas CLI | Pi tools have no universal approval mode | Passed in `ask` mode |

Both initial replies and both resumed replies were exactly `NEBULA7319`.
For the tool test, a local file contained `RIVER2961`; the prompt did not contain
that value. Native tool events and the assistant's subsequent answer contained
the actual file contents. OpenCode's read permission was explicitly allowed with
`atlas permissions allow` before the native tool completed.

**Model limitation observed:** Pi's first read request was a follow-up in the
conversation whose earlier prompts said not to use tools. The small model replied
`Read file proof.txt.` without calling a tool. A fresh Pi conversation with a
direct read instruction executed the native tool and returned the correct file
contents. Both attempts are retained in the evidence. This smoke test does not
establish this 0.6B model's reliability for complex agent tasks.

Machine-readable evidence: [local-model-results.json](verification/local-model-results.json).
Its temporary paths and native handles record the executed test; the temporary
model, runtime, configurations and sessions were removed afterward.

## Pinned runtime and model

| Artifact | Identity | Download size |
| --- | --- | --- |
| llama.cpp CPU runtime | `b11761`, commit `119f68d7d441d66bdf8ebd117ca0deee2119fbcc` | 22,236,133 bytes |
| Official Qwen GGUF | `Qwen/Qwen3-0.6B-GGUF`, `Qwen3-0.6B-Q8_0.gguf` | 639,446,688 bytes |
| Model repository revision | `23749fefcc72300e3a2ad315e1317431b06b590a` | — |

SHA-256 checksums were verified before extracting/executing the runtime or loading
the model:

```text
42a03b2f80cc970d7042f06fe5ea74b410625497226adbd93cdb0268518222cc  llama-b11761-bin-ubuntu-x64.tar.gz
9465e63a22add5354d9bb4b99e90117043c7124007664907259bd16d043bb031  Qwen3-0.6B-Q8_0.gguf
```

Primary sources: [official llama.cpp release](https://github.com/ggml-org/llama.cpp/releases/tag/b11761),
[release asset metadata and digest](https://api.github.com/repos/ggml-org/llama.cpp/releases/tags/b11761),
[official Qwen model at tested revision](https://huggingface.co/Qwen/Qwen3-0.6B-GGUF/tree/23749fefcc72300e3a2ad315e1317431b06b590a),
[model file and checksum](https://huggingface.co/Qwen/Qwen3-0.6B-GGUF/blob/23749fefcc72300e3a2ad315e1317431b06b590a/Qwen3-0.6B-Q8_0.gguf).

## Reproduce the setup

Prerequisites are a working Go toolchain and installed Pi/OpenCode executables.
Use a new temporary directory, a private Atlas home (`chmod 700`), and separate
project/native configuration directories. Build the CLI from current source:

```bash
ATLAS_TEST_ROOT=$(mktemp -d /tmp/atlas-real-model-XXXXXXXX)
mkdir -m 700 "$ATLAS_TEST_ROOT/atlas-home"
go build -o "$ATLAS_TEST_ROOT/atlas" ./cmd/atlas
```

Download the two pinned official artifacts, and compare their SHA-256 hashes to
the values above before use:

```text
https://github.com/ggml-org/llama.cpp/releases/download/b11761/llama-b11761-bin-ubuntu-x64.tar.gz
https://huggingface.co/Qwen/Qwen3-0.6B-GGUF/resolve/23749fefcc72300e3a2ad315e1317431b06b590a/Qwen3-0.6B-Q8_0.gguf
```

After extracting the runtime beneath the temporary directory, launch its server
with an available local port. The recorded test used port 37567 and these options
(substitute your own available port and paths):

```bash
llama-server --model Qwen3-0.6B-Q8_0.gguf \
  --host 127.0.0.1 --port 37567 --alias qwen3-0.6b \
  --ctx-size 16384 --parallel 1 --threads 8 --n-predict 512 \
  --reasoning off --chat-template-kwargs '{"enable_thinking":false}' \
  --jinja --temp 0 --seed 42
```

The explicit `enable_thinking` template argument was accepted but deprecated in
this runtime; `--reasoning off` is its supported replacement. The server stayed
on IPv4 loopback and served the compatible API at `http://127.0.0.1:37567/v1`.

### Isolated Pi profile

Configure the Atlas `pi` profile's `command` with the absolute installed Pi
executable and environment overrides pointing to temporary directories:

```json
{
  "adapter": "pi",
  "command": ["/absolute/path/to/pi"],
  "env": {
    "HOME": "/temporary/pi-native/home",
    "PI_CODING_AGENT_DIR": "/temporary/pi-native/agent",
    "PI_CODING_AGENT_SESSION_DIR": "/temporary/pi-native/sessions",
    "PI_OFFLINE": "1",
    "PI_TELEMETRY": "0"
  },
  "options": {"offline": true}
}
```

Write `models.json` in that isolated Pi agent directory:

```json
{
  "providers": {
    "atlaslocal": {
      "baseUrl": "http://127.0.0.1:37567/v1",
      "api": "openai-completions",
      "apiKey": "local-only",
      "models": [{
        "id": "qwen3-0.6b",
        "name": "Qwen3 0.6B Q8_0 local real inference",
        "reasoning": false,
        "input": ["text"],
        "contextWindow": 16384,
        "maxTokens": 512
      }]
    }
  }
}
```

The placeholder key is for this unauthenticated loopback server. It is not a
credential for a hosted provider. Pi settings selected this model, thinking `off`,
and disabled automatic retry/compaction for the bounded smoke test.

### Isolated OpenCode profile

Use the absolute installed native executable, with temporary `HOME`,
`XDG_CONFIG_HOME`, `XDG_DATA_HOME`, `XDG_STATE_HOME`, and `XDG_CACHE_HOME` environment
overrides. Absolute executable paths avoid a version-manager shim resolving
against the temporary home. Set `OPENCODE_DISABLE_AUTOUPDATE=true` and supply this
JSON as the profile's `OPENCODE_CONFIG_CONTENT` environment value:

```json
{
  "model": "atlaslocal/qwen3-0.6b",
  "provider": {
    "atlaslocal": {
      "npm": "@ai-sdk/openai-compatible",
      "name": "Atlas local llama",
      "options": {
        "baseURL": "http://127.0.0.1:37567/v1",
        "apiKey": "local-only"
      },
      "models": {
        "qwen3-0.6b": {
          "name": "Qwen3 0.6B Q8_0 local real inference",
          "limit": {"context": 16384, "output": 512}
        }
      }
    }
  }
}
```

Put both profiles under `harnesses` in the temporary Atlas `config.json` with
`schemaVersion: 1`. Set `ATLAS_HOME` to that private directory before running the
commands below. The test used separate project directories for each harness.

## CLI operations exercised

For Pi, from its isolated project directory:

```bash
atlas init --harness pi --template researcher
atlas models --harness pi --json
atlas run --harness pi --model atlaslocal/qwen3-0.6b \
  --trust-project --thinking off --background \
  'Remember the exact codeword NEBULA7319 for this conversation. Reply with only NEBULA7319. Do not use tools. /no_think'
atlas wait SESSION_ID --timeout 90s --json
atlas logs SESSION_ID --json
atlas send SESSION_ID \
  'What is the exact codeword I asked you to remember in my previous message? Reply only with that codeword. Do not use tools. /no_think' \
  --background
atlas wait SESSION_ID --timeout 90s --json
atlas logs SESSION_ID --json --after FIRST_TURN_FINAL_SEQUENCE
```

Repeat in the OpenCode project with `--harness opencode`; replace Pi's
`--trust-project --thinking off` with `--permission-mode deny` for the text-only
conversation. `--trust-project` is specifically a Pi option.

For the OpenCode file-read check, create `proof.txt` containing `RIVER2961`, then:

```bash
atlas configure SESSION_ID --permission-mode ask
atlas send SESSION_ID \
  'Read the file proof.txt using the read tool and reply with its exact contents. You must call the tool to inspect the file. /no_think' \
  --background
atlas permissions list SESSION_ID
atlas permissions allow SESSION_ID READ_PERMISSION_ID
atlas wait SESSION_ID --timeout 90s
atlas logs SESSION_ID --json
```

The successful fresh Pi read used a new managed conversation with this prompt:

```text
Use your read tool to read proof.txt now. After the actual tool result arrives, reply with the exact file contents. You cannot know the contents until you call the read tool. /no_think
```

Stop the temporary Atlas daemon, terminate and reap the temporary llama.cpp
process, then remove only the temporary directory created for this test. The
recorded run performed that cleanup. No runtime/model was installed globally, and
no existing provider credentials or native session stores were modified.

## Scope

This proves actual local inference, native durable context restoration and a
small real tool interaction through the CLI on the tested Linux machine. The
separate deterministic tests cover broader protocol/error/cancellation/MCP cases.
It does not prove authentication to a company's hosted endpoint, external billing,
large-model quality, or all provider-specific SDK behaviors.
