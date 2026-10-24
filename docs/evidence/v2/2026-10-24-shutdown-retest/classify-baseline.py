from pathlib import Path
from collections import Counter
import json
f=Path('docs/evidence/v2/2026-10-24-shutdown-retest')
server=[]
for line in (f/'server-baseline.log').read_text().splitlines():
 if 'SHUTDOWN ' in line: server.append(json.loads(line.split('SHUTDOWN ',1)[1]))
rows=[]
for line in (f/'baseline-console.log').read_text().splitlines():
 try:rows.append(json.loads(line))
 except ValueError:pass
results=[]
for row in rows:
 events=[]
 for line in (f/(row['name']+'.log')).read_text().splitlines():
  if 'LOAD_SHUTDOWN ' in line:events.append(json.loads(line.split('LOAD_SHUTDOWN ',1)[1]))
 cut=next(e['atNs'] for e in events if e['event']=='cancel_all')
 stopped=next(e['atNs'] for e in events if e['event']=='workers_stopped')
 cleanup=next(e['atNs'] for e in events if e['event']=='cleanup_begin')
 client_reads={e['participant']:e for e in events if e['event']=='read_end'}
 # Setup begins after the preceding case, and the runner waits1.25s for cleanup before starting the next.
 previous_end=results[-1]['cleanupBeginNs'] if results else 0
 errors=[e for e in server if e['event']=='write_error' and previous_end+1_000_000_000 < e['atNs'] < cleanup+1_000_000_000]
 classified=[]
 for e in errors:
  client=client_reads.get(e.get('participant'))
  cancellation_observed=bool(client and client['contextError']=='context canceled' and client['atNs']>=cut)
  after_cut=e['atNs']>=cut
  own_deadline=e['deadlineElapsed'] or e['isDeadline']
  result='client_abort_transport_failure' if cancellation_observed and after_cut and not own_deadline else 'unclassified_or_real_deadline'
  classified.append({'participant':e.get('participant'),'connection':e['connection'],'roomInstance':e.get('roomInstance'),'body':e['body'],'atNs':e['atNs'],'startedNs':e['startedNs'],'deadlineNs':e['deadlineNs'],'afterClientCancelMs':(e['atNs']-cut)/1e6,'ownDeadlineElapsed':own_deadline,'clientReadEnd':client,'serverReadEnd':e['readEnd'],'error':e['error'],'classification':result})
 expected=row['lifecycleMetricDelta']['writeErrors']
 results.append({'name':row['name'],'cancelAllNs':cut,'workersStoppedNs':stopped,'cleanupBeginNs':cleanup,'metricWriteErrors':expected,'traceWriteErrors':len(errors),'countsMatch':expected==len(errors),'measurementWriteErrors':row['metricDelta']['writeErrors'],'classifications':dict(Counter(e['classification'] for e in classified)),'bodies':dict(Counter(e['body'] for e in classified)),'errors':classified})
out={'profiles':results,'metricWriteErrors':sum(r['metricWriteErrors'] for r in results),'traceWriteErrors':sum(r['traceWriteErrors'] for r in results),'allCountsMatch':all(r['countsMatch'] for r in results),'classifications':dict(sum((Counter(r['classifications']) for r in results),Counter())),'historical139PerEventClassificationPossible':False,'historicalLimit':'Original139counter increments lacked per-event strings; this proves the same-source same-profile reproduced mechanism, not a one-to-one retrospective mapping.'}
(f/'baseline-classification.json').write_text(json.dumps(out,indent=2)+'\n')
print({k:v for k,v in out.items() if k not in ['profiles','historicalLimit']})
