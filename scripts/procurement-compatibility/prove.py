#!/usr/bin/env python3
"""Compile three exact revisions; only the two protected revisions exercise upgraded data."""
import hashlib
import json
import os
from pathlib import Path
import sqlite3
import subprocess
import tarfile
import tempfile
import time

HERE = Path(__file__).resolve().parent
REPO = HERE.parents[1]
REVISIONS = {
    'old': 'b0b08aec6853b0d48a508af90ac852fc628e6982',
    'baseline': '95829b96672bf20611cf2ca2c264df3062518bc0',
    'candidate': 'f833b9496d20112cf442593a2e8e706473553f57',
}

def sha(data): return hashlib.sha256(data).hexdigest()

def run(args, *, cwd, logfile, env=None):
    with logfile.open('ab') as log:
        result = subprocess.run(args, cwd=cwd, env=env, stdout=log, stderr=subprocess.STDOUT, timeout=900)
    if result.returncode:
        raise RuntimeError(f'Failed ({result.returncode}); inspect {logfile}')

def snapshot(db, columns=None):
    with sqlite3.connect(f'file:{db}?mode=ro', uri=True) as connection:
        if columns is None:
            tables = [r[0] for r in connection.execute("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name")]
            columns = {t:[r[1] for r in connection.execute('PRAGMA table_info("'+t+'")')] for t in tables}
        rows = {}
        for table, names in columns.items():
            query = 'SELECT '+','.join('"'+n+'"' for n in names)+' FROM "'+table+'" ORDER BY rowid'
            values = [[{'blob_sha256':sha(v)} if isinstance(v, bytes) else v for v in row] for row in connection.execute(query)]
            rows[table] = {'count':len(values),'typed_rows_sha256':sha(json.dumps(values,sort_keys=True,separators=(',',':')).encode())}
        return columns, rows

def main():
    root = Path(tempfile.mkdtemp(prefix='kw-procurement-compatibility-')).resolve()
    os.chmod(root,0o700)
    fixture = HERE/'fixture.go.txt'
    proof = {'schema':1,'started_utc':time.strftime('%Y-%m-%dT%H:%M:%SZ',time.gmtime()),'fixture_sha256':sha(fixture.read_bytes()),'revisions':{},'steps':[],'scope':'synthetic SQLite; local supplier HTTP fixtures only; application callback boundary; no old binary after upgrade'}
    print(root, flush=True)
    for label, revision in REVISIONS.items():
        actual = subprocess.check_output(['git','rev-parse',revision],cwd=REPO,text=True).strip()
        if actual != revision: raise ValueError('Revision mismatch')
        source=root/label;source.mkdir()
        archive=root/(label+'.tar')
        with archive.open('wb') as stream: subprocess.run(['git','archive','--format=tar',revision],cwd=REPO,stdout=stream,check=True)
        with tarfile.open(archive) as tar:
            if any(member.name.startswith('/') or '..' in Path(member.name).parts for member in tar.getmembers()):raise ValueError('Invalid archive path')
            tar.extractall(source)
        overlay=root/(label+'-overlay.json')
        overlay.write_text(json.dumps({'Replace':{str(source/'internal/modules/procurement/integrationtest/exact_version_compatibility_test.go'):str(fixture)}}))
        binary=root/(label+'-fixture')
        run(['go','test','-race','-overlay='+str(overlay),'-c','-o',str(binary),'./internal/modules/procurement/integrationtest'],cwd=source,logfile=root/(label+'-build.log'))
        proof['revisions'][label]={'commit':revision,'archive_sha256':sha(archive.read_bytes()),'binary_sha256':sha(binary.read_bytes()),'build_log':label+'-build.log'}
    database=root/'synthetic.db'
    def phase(label, action, role=''):
        log=root/(str(len(proof['steps'])+1)+'-'+label+'-'+action+'.log')
        env={**os.environ,'KW_COMPAT_DB':str(database),'KW_COMPAT_PHASE':action,'KW_COMPAT_ROLE':role}
        run([str(root/(label+'-fixture')),'-test.run=^TestExactVersionCompatibility$','-test.v','-test.timeout=60s'],cwd=root/label,logfile=log,env=env)
        proof['steps'].append({'revision':label,'phase':action,'role':role,'passed':True,'log':log.name})
        print(label,action,role,'passed',flush=True)
    phase('old','seed-old')
    columns, before=snapshot(database)
    if 'submission_version' in columns['procurement_orders']:raise ValueError('Seed is not the original schema')
    phase('baseline','migrate')
    _,after=snapshot(database,columns)
    if before!=after:raise ValueError('Old-column data changed during real schema migration')
    proof['migration']={'old_columns':columns,'old_rows_before':before,'old_rows_after':after,'preserved':True}
    phase('baseline','fixture-intents')
    with sqlite3.connect(database) as db:
        def intent_rows():return db.execute("SELECT id,local_order_no,status,submission_version,submitted_at,submission_request_sha256,submission_connection_sha256 FROM procurement_orders WHERE local_order_no LIKE 'BLOCK-%' ORDER BY id").fetchall()
        original_intents=intent_rows()
    phase('baseline','exercise','baseline')
    current_columns,current_rows=snapshot(database)
    phase('candidate','migrate')
    if snapshot(database,current_columns)[1]!=current_rows:raise ValueError('Candidate migration changed persisted business data')
    proof['candidate_migration_preserved']=True
    with sqlite3.connect(database) as db:
        indexes = db.execute("PRAGMA index_list('procurement_orders')").fetchall()
        if not any(row[1]=='idx_procurement_local_order_unique' and row[2]==1 and row[4]==0 for row in indexes):
            raise ValueError('Global local-order unique index missing')
        proof['global_local_order_unique_index']=True
    phase('candidate','exercise','candidate')
    phase('baseline','exercise','baseline')
    with sqlite3.connect(database) as db:
        current=db.execute("SELECT id,local_order_no,status,submission_version,submitted_at,submission_request_sha256,submission_connection_sha256 FROM procurement_orders WHERE local_order_no LIKE 'BLOCK-%' ORDER BY id").fetchall()
        if original_intents!=current:raise ValueError('Version switch changed unresolved intents')
        if db.execute('SELECT COUNT(*) FROM fulfillments').fetchone()[0]!=5:raise ValueError('Cross-version duplicate fulfillment')
        proof['result']={'unresolved_intents_preserved':True,'new_exactly_once_fulfillments':4,'legacy_fulfillments_preserved':1,'normal_order_and_admin_reads':True,'baseline_candidate_baseline':True,'old_binary_never_runs_after_migration':True}
    proof['completed_utc']=time.strftime('%Y-%m-%dT%H:%M:%SZ',time.gmtime())
    (root/'proof.json').write_text(json.dumps(proof,indent=2,sort_keys=True)+'\n')
    print(root/'proof.json',flush=True)

if __name__=='__main__':main()
