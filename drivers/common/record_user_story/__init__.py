"""Official SDK story orchestration using the Go agent-sdk-testify tool."""

import os

_version = os.environ.get("AGENT_SDK_TESTIFY_VERSION", "0+unknown")
__version__ = "0+unknown" if _version == "devel" else _version.removeprefix("v")
