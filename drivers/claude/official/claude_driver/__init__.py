"""Claude official-SDK driver entry point."""

def main():
    from record_user_story.driver import main as run
    from .cases import CASES
    return run(expected_provider='claude', case_registry=CASES)
