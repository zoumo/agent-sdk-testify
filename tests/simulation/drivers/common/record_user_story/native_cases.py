"""Native case selection. Shared material JSON contains data, not execution code.

Provider adapters implement these native flows and their assertions. Keeping
this dispatch in the driver lets another SDK language use idiomatic test code.
"""

# Case IDs are public test identities, not an arithmetic encoding of behavior.
def prepare_case(plan, cases):
    profile = cases.get(plan['caseId'])
    if profile is None:
        raise NotImplementedError('case is not implemented by this driver')
    definition = dict(plan['scenario'])
    definition.pop('steps', None)
    definition['driverProfile'] = profile
    definition['submission'] = 'progress-barrier' if profile == 'active-input' else 'sequential-results'
    definition['progressAction'] = {'active-input': 'submit-supplement', 'interrupt': 'interrupt'}.get(profile)
    definition.setdefault('configuration', {})
    definition.setdefault('requiredRequests', [])
    definition.setdefault('parameters', {})
    definition.setdefault('mode', 'live-selected-path')
    definition.setdefault('case', plan.get('case', {}))
    plan['scenario'] = definition
    plan['driverProfile'] = profile
    plan.setdefault('stdinEOFAction', 'close')
