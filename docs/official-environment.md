# Official record/verify environment

Only record/verify need Python official SDKs. Mock requires only the installed tool.
Build `drivers/` with Python 3.11+, pip and setuptools>=68; install the resulting
wheel with `--no-deps` into an explicit environment. Add that environment's bin to PATH.
Do not install the historical requirements.lock as a currently available PyPI lock:
`openai-codex==0.161.0` and `openai-codex-cli-bin` are unavailable there. Normal VCS dependency resolution also fails when the source requests CLI-bin 0.153.4. It remains historical provenance.

The verified parent environment is `.artifacts/official-env/bin` in the main tool
checkout. Its actual metadata is Claude `0.2.174`, Codex **`0.0.0.dev0`**. The latter
is the normalized metadata of official Codex source `rust-v0.161.0`, revision
`979011409de0a60b52f179721948e65531d26144`, whose pyproject declares `0.0.0-dev`.
Do not relabel this distribution as 0.161.0 or change a recording to match it.

Reproduce the source installation in an explicitly selected environment (network
preparation is a separate operator action, never performed by verify):

```sh
python3.11 -m venv /absolute/official-env
/absolute/official-env/bin/python -m pip install 'setuptools>=68' wheel 'uv_build>=0.11.19,<0.12' 'pydantic>=2.12' 'packaging>=26.2' 'claude-agent-sdk==0.2.174'
git clone --branch rust-v0.161.0 https://github.com/openai/codex.git /absolute/codex-source
git -C /absolute/codex-source rev-parse HEAD
# Must print 979011409de0a60b52f179721948e65531d26144 before installing.
/absolute/official-env/bin/python -m pip install --no-deps --no-build-isolation /absolute/codex-source/sdk/python
/absolute/official-env/bin/python -m pip install --no-deps /absolute/agent_sdk_testify_driver-0.1.0-py3-none-any.whl
/absolute/official-env/bin/python -c 'from importlib.metadata import version; print(version("openai-codex")); print(version("claude-agent-sdk"))'
```

`--no-deps` intentionally avoids installing the source package's separate native
CLI dependency. Supply an explicit native executable to record (`--cli ABS`).
Verify supplies its own strict mock/proxy executable through the official driver;
never allow discovery to select a real Codex or Claude fallback. The source SDK,
recorded native CLI and installed tool have distinct version identities.
A native `--version`/`-v` query reads recorded metadata and never consumes a mock
allocation. The tool's `--version` still reports the actual tool version.

Parent-supplied existing evidence, not rerun or promoted by T8: fresh official
Claude verification has 33 passes and CASE-003 untriggered; fresh Codex has 33
first-message version mismatches. These diagnostics do not rewrite the 64 historical
qualified builtin candidates (32/provider) or the complete 72-entry coverage inventory.
The parent's actual official Claude builtin CASE-001 smoke passed ready/done and
finish after native-version fix c097894, at
`.artifacts/builtin-official-smoke/claude-fixed-_kdd34w_/run` in the main checkout.
No new recording or live Agent/model call is authorized by these instructions.
Codex required CASE-012 remains blocked; optional failed/unsupported/unverified
entries remain visible and the full required gate remains failed.
