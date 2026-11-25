"""Pi official RPC client driver entry point."""


def main():
    from record_user_story.driver import main as run
    from .base import BASE_CASES
    return run(expected_provider="pi", case_registry={"SDK-CASE-001": "continuation", **BASE_CASES})
