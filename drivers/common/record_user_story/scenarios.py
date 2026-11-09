"""Interpret language-neutral JSON scenarios. No native SDK imports."""
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import re
import stat


def scenario_directory():
    configured = os.environ.get('AGENT_MOCK_SCENARIOS')
    return Path(configured) if configured else Path(__file__).parent / 'examples'


def valid_identity(value):
    return isinstance(value, str) and re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9_-]{0,127}', value) is not None


def scenario_assets(directory=None):
    """Files needed to consume the configuration without the source checkout."""
    root = directory or scenario_directory()
    return sorted(path for path in root.rglob('*.json') if path.is_file())


def _unique(pairs):
    value = {}
    for key, item in pairs:
        if key in value:
            raise ValueError('duplicate scenario JSON key: ' + key)
        value[key] = item
    return value


def _read(path):
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    try:
        info = os.fstat(fd)
        if not stat.S_ISREG(info.st_mode):
            raise ValueError('scenario must be a regular JSON file')
        with os.fdopen(fd, 'r', closefd=False) as source:
            return json.load(source, object_pairs_hook=_unique,
                             parse_constant=lambda value: (_ for _ in ()).throw(ValueError('non-JSON constant: ' + value)))
    finally:
        os.close(fd)


def _validate(value, schema, path='$'):
    """Validate the JSON Schema keywords used by our portable schema files.

    The files are also consumable by standard Draft 2020-12 validators. No
    language-specific expressions or executable configuration are supported.
    """
    kinds = {'object': dict, 'array': list, 'string': str, 'integer': int, 'boolean': bool, 'null': type(None)}
    if 'oneOf' in schema:
        matches = 0
        for branch in schema['oneOf']:
            try:
                _validate(value, branch, path)
                matches += 1
            except ValueError:
                pass
        if matches != 1:
            raise ValueError(path + ': expected exactly one schema branch')
    expected = schema.get('type')
    if expected:
        choices = expected if isinstance(expected, list) else [expected]
        if not any(type(value) is kinds[kind] for kind in choices):
            raise ValueError(path + ': invalid JSON type')
    if 'const' in schema and (type(value) is not type(schema['const']) or value != schema['const']):
        raise ValueError(path + ': unsupported schema version or constant')
    if 'enum' in schema and value not in schema['enum']:
        raise ValueError(path + ': unsupported value')
    if isinstance(value, str):
        if len(value) < schema.get('minLength', 0):
            raise ValueError(path + ': empty string')
        if 'pattern' in schema and re.search(schema['pattern'], value) is None:
            raise ValueError(path + ': invalid identity pattern')
    if 'minimum' in schema and value < schema['minimum']:
        raise ValueError(path + ': below minimum')
    if 'maximum' in schema and value > schema['maximum']:
        raise ValueError(path + ': above maximum')
    if isinstance(value, dict):
        if set(schema.get('required', [])) - value.keys():
            raise ValueError(path + ': missing fields')
        properties = schema.get('properties', {})
        additional = schema.get('additionalProperties', True)
        for name, item in value.items():
            if name in properties:
                _validate(item, properties[name], path + '.' + name)
            elif additional is False:
                raise ValueError(path + ': unknown field ' + name)
            elif isinstance(additional, dict):
                _validate(item, additional, path + '.' + name)
    if isinstance(value, list):
        if len(value) < schema.get('minItems', 0):
            raise ValueError(path + ': empty selection')
        if 'maxItems' in schema and len(value) > schema['maxItems']:
            raise ValueError(path + ': too many entries')
        if schema.get('uniqueItems') and len({json.dumps(item, sort_keys=True) for item in value}) != len(value):
            raise ValueError(path + ': duplicate entries')
        for index, item in enumerate(value):
            _validate(item, schema.get('items', {}), path + '[' + str(index) + ']')


def _document(directory, filename, schema):
    value = _read(directory / filename)
    schema_path = directory / (schema + '.schema.json')
    if not schema_path.exists():
        schema_path = Path(__file__).parent / 'schemas' / (schema + '.schema.json')
    _validate(value, _read(schema_path))
    return value


def story_ids(directory=None):
    directory = directory or scenario_directory()
    values = _document(directory, 'index.json', 'index')['stories']
    if not all(valid_identity(value) for value in values):
        raise ValueError('invalid story identity')
    return values



def story_documents(directory=None):
    directory = directory or scenario_directory()
    stories, seen = [], set()
    for identity in story_ids(directory):
        story = _document(directory, identity + '.json', 'scenario')
        if story['id'] != identity:
            raise ValueError('story identity mismatch')
        for case in story['cases']:
            if not valid_identity(case['id']) or case['storyId'] != identity or case['id'] in seen:
                raise ValueError('case has duplicate identity or wrong story owner')
            seen.add(case['id'])
        stories.append(story)
    return stories


def case_recipes(provider, stories=None, cases=None, directory=None):
    documents = story_documents(directory)
    selected_stories = stories.split(',') if isinstance(stories, str) else stories
    selected_cases = cases.split(',') if isinstance(cases, str) else cases
    known_stories = {story['id'] for story in documents}
    known_cases = {case['id'] for story in documents for case in story['cases']}
    if selected_stories is not None and (not selected_stories or set(selected_stories) - known_stories):
        raise ValueError('unknown stable story ID')
    if selected_cases is not None and (not selected_cases or set(selected_cases) - known_cases):
        raise ValueError('unknown independent case ID')
    result = []
    for story in documents:
        if selected_stories is not None and story['id'] not in selected_stories:
            continue
        for case in story['cases']:
            if selected_cases is None or case['id'] in selected_cases:
                result.append(recipe(provider, story['id'], directory, case_id=case['id']))
    if not result:
        raise ValueError('story and case selection have no intersection')
    return result


def select_story_groups(selection, dataset=None):
    if dataset is None:
        known = set(story_ids())
    else:
        paths = list(Path(dataset).glob('*/*/plan.json'))
        plans = [_read(path) for path in paths]
        known = {plan.get('storyId') for plan in plans}
    selected = selection.split(',')
    if not selected or set(selected) - known:
        raise ValueError('unknown stable story ID; select case IDs with --cases')
    return ','.join(dict.fromkeys(selected))


def _workflow(core):
    """Compile the supported declarative workflow forms, independently of IDs."""
    steps = core['steps']
    if core['driverProfile'] == 'existing-children':
        if core['inputs'] or core['limits']['inputs'] != 0 or steps != [
                {'op': 'session.open'}, {'op': 'history.discover'}, {'op': 'session.close'}]:
            raise ValueError('existing-child observation must not generate new input')
        return [], 'sequential-results', None
    if not steps:
        if core['inputs']:
            raise ValueError('inputs without workflow')
        return [], 'sequential-results', None
    submitted = [step['input'] for step in steps if step['op'] == 'input.submit' and 'input' in step]
    if len(set(submitted)) != len(submitted) or set(submitted) != core['inputs'].keys():
        raise ValueError('workflow must reference each declared input exactly once')
    opening = [{'op': 'session.open'}]
    if {'op': 'configuration.apply'} in steps:
        opening.append({'op': 'configuration.apply'})
    result = {'op': 'event.wait', 'event': 'execution.result', 'onMissing': 'untriggered'}
    closing = [{'op': 'session.close'}]
    progress = {'op': 'event.wait', 'event': 'execution.progress', 'onMissing': 'untriggered'}
    if progress in steps:
        if len(submitted) not in (1, 2):
            raise ValueError('unsupported progress workflow')
        action = {'op': 'execution.interrupt'} if len(submitted) == 1 else {'op': 'input.submit', 'input': submitted[1]}
        expected = opening + [{'op': 'input.submit', 'input': submitted[0]}, progress, action, result] + closing
        trigger = 'interrupt' if len(submitted) == 1 else 'submit-supplement'
        submission = 'progress-barrier' if len(submitted) == 2 else 'sequential-results'
    else:
        expected = opening + [step for name in submitted for step in ({'op': 'input.submit', 'input': name}, result)] + closing
        submission, trigger = 'sequential-results', None
    if steps != expected:
        raise ValueError('unsupported workflow order or fields')
    if core['limits']['inputs'] != len(submitted):
        raise ValueError('workflow input limit differs from declared submissions')
    return submitted, submission, trigger


def recipe(provider, story, directory=None, case_id=None):
    directory = directory or scenario_directory()
    if not re.fullmatch(r'[a-z][a-z0-9-]*', provider):
        raise ValueError('unknown provider')
    documents = story_documents(directory)
    container = next((item for item in documents if item['id'] == story), None)
    if container is None:
        raise ValueError('unknown story')
    if case_id is None:
        implemented = [case for case in container['cases'] if case['implementation'] == 'implemented']
        if len(implemented) != 1:
            raise ValueError('select an explicit case ID for this story')
        core = implemented[0]
    else:
        core = next((case for case in container['cases'] if case['id'] == case_id), None)
        if core is None:
            raise ValueError('case does not belong to selected story')
    case = {'id': core['id'], 'storyId': story,
            'scope': core.get('expect', {}).get('scope', core['goal']),
            'outOfScope': core.get('expect', {}).get('outOfScope', ['other case targets'])}
    identity = {'version': container['version'], 'provider': provider, 'story': story,
                'storyId': story, 'caseId': core['id'], 'storyGoal': container['goal'],
                'goal': core['goal'], 'required': core['required'], 'implementation': core['implementation'],
                'case': case}
    binding_file = _document(directory, 'bindings/' + provider + '.json', 'bindings')
    known_cases = {item['id'] for doc in documents for item in doc['cases']}
    if binding_file['provider'] != provider or set(binding_file['cases']) - known_cases:
        raise ValueError('provider binding identity mismatch')
    if core['id'] not in binding_file['cases']:
        raise ValueError('case has no provider binding')
    binding = binding_file['cases'][core['id']]
    identity['required'] = binding.get('required', core['required'])
    execution = dict(core['execution'], **binding.get('execution', {}))
    if execution['mode'] != 'live-selected-path' and not execution['reason'].strip():
        raise ValueError('unavailable simulation case requires a reason')
    if core['implementation'] == 'planned':
        if execution['mode'] == 'live-selected-path':
            raise ValueError('planned case cannot have a live execution route')
        return dict(identity, bundle=None, driverProfile=None, mode=execution['mode'],
                    reason=execution['reason'], prompts=[], files={},
                    submission='sequential-results', progressAction=None, requiredRequests=[], progressBarrier=None,
                    answers={}, configuration={}, steps=[], limits={'inputs': 0}, scope=case['scope'])
    from .policy import DRIVER_PROFILES
    if core['driverProfile'] not in DRIVER_PROFILES:
        raise ValueError('unsupported native driver profile')
    if not core['expect']['scope'].strip() or any(not value.strip() for value in core['expect']['outOfScope']):
        raise ValueError('case scope must be explicit')
    submitted, submission, trigger = _workflow(core)
    execution = dict(core['execution'], **binding.get('execution', {}))
    mode = execution['mode']
    prompts = binding.get('inputs', [core['inputs'][name] for name in submitted]) if mode == 'live-selected-path' else []
    if mode == 'live-selected-path' and ((not prompts and core['driverProfile'] != 'existing-children') or len(prompts) != len(submitted)):
        raise ValueError('provider inputs differ from workflow submissions')
    configuration = binding['configuration']
    if bool(configuration) != ({'op': 'configuration.apply'} in core['steps']):
        raise ValueError('configuration binding differs from workflow')
    if configuration and set(configuration) != {'timing', 'modelSource', 'thinking', 'effectiveValue'}:
        raise ValueError('incomplete configuration binding')
    if configuration and ((provider == 'codex' and configuration['modelSource'] != 'session') or
                          (provider == 'claude' and (configuration['modelSource'] != 'prepared-or-default' or configuration['thinking'] is not None))):
        raise ValueError('configuration operation unsupported by Python adapter')
    answers = dict(core['expect'].get('answers', {}), **binding['answers'])
    if 'question.answer' in core['expect']['requests']:
        choice = answers.get('choice')
        choices = choice if isinstance(choice, list) else [choice]
        if (not isinstance(answers.get('options'), list) or not choices
                or any(not isinstance(value, str) or not value for value in choices)
                or len(set(choices)) != len(choices)
                or (core.get('parameters', {}).get('freeText') and not isinstance(choice, str))
                or (not core.get('parameters', {}).get('freeText')
                    and any(value not in answers['options'] for value in choices))):
            raise ValueError('question answer is not a configured option')
    files = dict(core['setup']['files'], **binding['files'])
    for name in files:
        path = PurePosixPath(name)
        if not name or path.is_absolute() or '..' in path.parts or '\\' in name or str(path) != name:
            raise ValueError('scenario file escapes project')
    requests = binding['requests']
    if set(requests) != set(core['expect']['requests']):
        raise ValueError('native request bindings differ from expected interactions')
    parameters = dict(core.get('parameters', {}), **binding.get('parameters', {}))
    if ({'effort', 'targetEffort'} & parameters.keys()) and parameters.get('setting') != 'effort':
        raise ValueError('effort values require the effort setting')
    if parameters.get('setting') == 'effort':
        initial, target = parameters.get('effort'), parameters.get('targetEffort')
        if (parameters.get('configurationTiming') != 'between-inputs'
                or not isinstance(initial, str) or not initial
                or not isinstance(target, str) or not target
                or initial == target):
            raise ValueError('effort change requires distinct creation and runtime effort values')
    result = dict(identity, bundle=core['driverProfile'], driverProfile=core['driverProfile'], mode=mode,
                reason=execution['reason'], prompts=prompts, files=files, submission=submission,
                progressAction=trigger, requiredRequests=[requests[name] for name in core['expect']['requests']],
                progressBarrier=binding['progressBarrier'], answers=answers, configuration=configuration,
                steps=core['steps'] if mode == 'live-selected-path' else [],
                limits=dict(core['limits'], inputs=len(prompts)), scope=case['scope'])
    if 'parameters' in core or 'parameters' in binding:
        result['parameters'] = parameters
    return result


def summarize_story(provider, story_id, results, evidence_layer, directory=None):
    cases = [item for item in case_recipes(provider, stories=[story_id], directory=directory) if item['required']]
    missing, failed = [], []
    for case in cases:
        result = results.get(case['caseId'], {})
        if case['mode'] != 'live-selected-path':
            missing.append(case['caseId'])
        elif result.get('status') == 'failed' and result.get('evidenceLayer') == evidence_layer:
            failed.append(case['caseId'])
        elif result.get('status') != 'passed' or result.get('evidenceLayer') != evidence_layer:
            missing.append(case['caseId'])
    status = 'failed' if failed else ('partial' if missing else 'passed')
    return {'storyId': story_id, 'provider': provider, 'evidenceLayer': evidence_layer,
            'status': status, 'requiredCases': len(cases), 'missingCases': missing, 'failedCases': failed}


def digest(value):
    return hashlib.sha256(json.dumps(value, sort_keys=True, separators=(',', ':')).encode()).hexdigest()


def plan_recipe(plan):
    required = ('storyId', 'caseId', 'driverProfile', 'scenario', 'scenarioSHA256', 'prompts')
    if (plan.get('version') != 4 or any(name not in plan for name in required)
            or plan.get('story') != plan['storyId']):
        raise ValueError('current simulation plan requires explicit story, case and profile')
    expected = recipe(plan['provider'], plan['storyId'], case_id=plan['caseId'])
    if plan['driverProfile'] != expected['driverProfile']:
        raise ValueError('simulation driver profile differs from case')
    return expected


def validate_recipe(plan):
    if plan.get('_nativeCase'):
        return plan['scenario']
    expected = plan_recipe(plan)
    if plan.get('scenario') != expected or plan.get('scenarioSHA256') != digest(expected):
        raise ValueError('scenario definition changed or unsupported')
    if plan['prompts'] != expected['prompts']:
        raise ValueError('scenario prompts changed')
    return expected


class Untriggered(ValueError):
    """Required native evidence was absent; do not retry to force it."""


class Flow:
    """Own only recording steps; native SDKs keep requests and answer authority."""
    def __init__(self, definition):
        self.definition = definition
        self.trace = []
        self.sent = 0
        self.completed = 0
        self.active = False
        self.triggered = False
        self.requests = []
        self.issues = []

    def inputs(self):
        prompts = self.definition['prompts']
        return prompts[:1] if self.definition['submission'] == 'progress-barrier' else prompts

    def submit(self):
        if self.active or self.sent >= len(self.definition['prompts']):
            raise ValueError('input outside scenario order/budget')
        self.trace.append({'step': 'submit', 'input': self.sent})
        self.sent += 1
        self.active = True

    def progress(self):
        action = self.definition['progressAction']
        if not self.active or not action or self.triggered:
            return None
        self.triggered = True
        self.trace.append({'step': 'progress-barrier', 'action': action})
        return action

    def action(self, action, outcome):
        if not self.active or not self.triggered or action != self.definition['progressAction']:
            raise ValueError('action without native progress barrier')
        if any(item.get('step') == action for item in self.trace):
            raise ValueError('scenario action repeated')
        if outcome not in ('accepted', 'rejected', 'written'):
            raise ValueError('unknown native action outcome')
        if action == 'submit-supplement':
            if self.sent >= len(self.definition['prompts']):
                raise ValueError('supplement input budget exhausted')
            self.sent += 1
        self.trace.append({'step': action, 'outcome': outcome})

    def request(self, name):
        expected = self.definition['requiredRequests']
        if not self.active or self.requests != expected[:len(self.requests)] or len(self.requests) >= len(expected) or name != expected[len(self.requests)]:
            self.issues.append('unexpected or repeated scenario request: ' + name)
        self.requests.append(name)
        self.trace.append({'step': 'request', 'name': name})

    def result(self):
        if not self.active:
            raise ValueError('result without submitted input')
        self.completed += 1
        self.active = False
        self.trace.append({'step': 'result', 'input': self.completed - 1})

    def post_result_event(self):
        if self.active or self.completed != 1 or self.sent != 1:
            raise ValueError('post-result observation must precede the next input')
        self.trace.append({'step': 'post-result-event'})

    def finish(self):
        missing = list(self.issues)
        if self.definition.get('parameters', {}).get('requirePostResultEvent') and {'step': 'post-result-event'} not in self.trace:
            missing.append('post-result-event')
        if self.definition['progressAction'] and not any(
                item['step'] == self.definition['progressAction'] for item in self.trace):
            missing.append(self.definition['progressAction'])
        missing.extend(name for name in self.definition['requiredRequests'] if name not in self.requests)
        # A supplemental native request can be rejected, or can share an existing
        # execution. Do not invent an extra result boundary for that input.
        required_results = len(self.inputs())
        if self.active or self.completed != required_results:
            missing.append('result-boundaries')
        if missing:
            raise Untriggered('required scenario evidence absent: ' + ', '.join(missing))
        self.trace.append({'step': 'close-drain'})
