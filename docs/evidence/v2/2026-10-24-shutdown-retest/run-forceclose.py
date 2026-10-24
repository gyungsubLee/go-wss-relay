from pathlib import Path
import json,hashlib,subprocess,time,urllib.request
root=Path.cwd();f=root/'docs/evidence/v2/2026-10-24-shutdown-retest'
token=(root/'.cache/live-load-20261023/operator.token').read_text().strip()
def stats():
 req=urllib.request.Request('http://127.0.0.1:19092/v2/status',headers={'Authorization':'Bearer '+token})
 with urllib.request.urlopen(req,timeout=5) as r:return json.load(r)
binary=root/'out/shutdown-forceclose-20261024/relay-load'
command=[str(binary),'--management','http://127.0.0.1:19092','--token-file','.cache/live-load-20261023/operator.token','--ca-file','.cache/live-load-20261023/tls/cert.pem','--participants','100','--room-size','8','--hz','10','--payload','679','--seconds','15','--warmup','2','--server-pid','71363','--slow-read-ms','500','--out',str(f/'baseline-slow-forceclose.json')]
before=stats();start=time.time_ns()
with (f/'baseline-slow-forceclose.log').open('x') as log:r=subprocess.run(command,stdout=log,stderr=subprocess.STDOUT,timeout=55)
time.sleep(1.25);after=stats()
out={'command':command,'serverPID':71363,'startedNs':start,'endedNs':time.time_ns(),'exitCode':r.returncode,'baseline':before,'cleanup':after,'writeErrors':after['writeErrors']-before['writeErrors'],'binarySha256':hashlib.sha256(binary.read_bytes()).hexdigest(),'sourceSha256':hashlib.sha256((root/'.cache/shutdown-retest/load-forceclose.go').read_bytes()).hexdigest()}
(f/'baseline-slow-forceclose-lifecycle.json').write_text(json.dumps(out,indent=2)+'\n');print({k:out[k] for k in ['exitCode','writeErrors','startedNs','endedNs']})
