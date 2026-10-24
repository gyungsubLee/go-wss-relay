"""Match the extra slow-reader diagnostic against individual client close calls."""
from collections import Counter
import json
from pathlib import Path


folder = Path(__file__).resolve().parent
window = json.loads((folder / 'baseline-slow-forceclose-lifecycle.json').read_text())


def events(path, marker):
    return [json.loads(line.split(marker, 1)[1])
            for line in path.read_text().splitlines() if marker in line]


client = events(folder / 'baseline-slow-forceclose.log', 'LOAD_SHUTDOWN ')
server = events(folder / 'server-baseline.log', 'SHUTDOWN ')
cancel_at = next(event['atNs'] for event in client if event['event'] == 'cancel_all')
errors = [event for event in server if event['event'] == 'write_error'
          and window['startedNs'] <= event['atNs'] <= window['endedNs']]
rows = []
for error in errors:
    participant = error['participant']
    own_events = [event for event in client if event.get('participant') == participant]
    read = next((event for event in own_events if event['event'] == 'read_end'), None)
    close = next((event for event in own_events if event['event'] == 'force_close'), None)
    classification = 'unknown'
    if not (error['deadlineElapsed'] or error['isDeadline']) and error['atNs'] >= cancel_at:
        if read and read['contextError'] == 'context canceled':
            classification = 'Read_context_cancel_closes_transport'
        elif close and close['atNs'] <= error['atNs']:
            classification = 'explicit_CloseNow_before_write_error'
    rows.append({
        'participant': participant,
        'classification': classification,
        'serverWrite': error,
        'clientReadEnd': read,
        'forceClose': close,
    })

result = {
    'metricWriteErrors': window['writeErrors'],
    'traceWriteErrors': len(errors),
    'countsMatch': window['writeErrors'] == len(errors),
    'classifications': dict(Counter(row['classification'] for row in rows)),
    'errors': rows,
}
(folder / 'forceclose-classification.json').write_text(json.dumps(result, indent=2) + '\n')
print({key: value for key, value in result.items() if key != 'errors'})
assert result['countsMatch']
assert 'unknown' not in result['classifications']
