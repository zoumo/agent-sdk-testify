"""Driver profiles, SDK identities and shared task instructions."""

SDK_VERSIONS = {'codex': ('openai-codex', '0.161.0'),
                'claude': ('claude-agent-sdk', '0.2.174')}
SYSTEM = 'Complete the supplied synthetic task in the working directory. Be concise.'

DRIVER_PROFILES = frozenset((
    'continuation', 'structured-input', 'active-input', 'fixture-tools',
    'children', 'background-children', 'approval', 'deferred-approval',
    'questions', 'configuration-timing', 'interrupt', 'session-close',
    'history', 'topology', 'existing-children', 'remote-close'))


def driver_profile(plan):
    profile = plan.get('driverProfile')
    if profile not in DRIVER_PROFILES:
        raise ValueError('current simulation plan requires an explicit driver profile')
    return profile
