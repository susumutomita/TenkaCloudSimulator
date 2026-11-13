#!/usr/bin/env python3
"""Local Finder/Fixer handoff ledger. Never executes findings or contacts a service."""
import argparse
import hashlib
import json
import re
import sqlite3
from pathlib import PurePosixPath
from factory_acceptance import SUITES, verify as verify_acceptance, validate_scope, candidate_paths

SHA = re.compile(r"[0-9a-f]{40}\Z")


def require(value, message):
    if not value:
        raise ValueError(message)


def scope(paths):
    require(isinstance(paths, list) and paths, "nonempty owned paths required")
    for p in paths:
        require(isinstance(p, str) and p and not p.startswith('/') and
                '..' not in PurePosixPath(p).parts and p == str(PurePosixPath(p)),
                "owned paths must be normalized repository-relative paths")
        require(not any(part.startswith('.') for part in PurePosixPath(p).parts),
                "hidden paths need separate human coordination")
    return sorted(set(paths))


def overlaps(a, b):
    return any(x == y or x.startswith(y + '/') or y.startswith(x + '/')
               for x in a for y in b)


class Factory:
    def __init__(self, filename):
        self.db = sqlite3.connect(filename, isolation_level=None, timeout=10)
        self.db.execute('''CREATE TABLE IF NOT EXISTS jobs (
            id TEXT PRIMARY KEY, body TEXT NOT NULL, state TEXT NOT NULL,
            owner TEXT, attempts INTEGER NOT NULL DEFAULT 0, head TEXT,
            evidence TEXT)''')
        self.db.execute('''CREATE TABLE IF NOT EXISTS audit (
            sequence INTEGER PRIMARY KEY, job TEXT NOT NULL, action TEXT NOT NULL,
            actor TEXT NOT NULL, reason TEXT NOT NULL, before TEXT NOT NULL,
            after TEXT NOT NULL, recorded_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP)''')

    def transaction(self, operation):
        self.db.execute('BEGIN IMMEDIATE')
        try:
            result = operation()
            self.db.execute('COMMIT')
            return result
        except Exception:
            self.db.execute('ROLLBACK')
            raise

    def get(self, key):
        row = self.db.execute('SELECT * FROM jobs WHERE id=?', (key,)).fetchone()
        require(row is not None, 'unknown finding')
        result = dict(zip(('id', 'body', 'state', 'owner', 'attempts', 'head', 'evidence'), row))
        result['fingerprint'] = hashlib.sha256(json.dumps(result, sort_keys=True).encode()).hexdigest()
        return result

    @staticmethod
    def validate(finding):
        require(isinstance(finding, dict), 'finding must be an object')
        for key in ('provider', 'operation', 'scenario', 'expected', 'actual', 'reference',
                    'reproduction', 'base', 'inventory_checked_at'):
            require(isinstance(finding.get(key), str) and finding[key].strip(), key + ' required')
        require(SHA.fullmatch(finding['base']), 'full base SHA required')
        require(isinstance(finding.get('related_work'), list), 'related_work inventory required')
        require(finding.get('oracle') in ('official-specification', 'recorded-cloud', 'local-regression'),
                'explicit oracle required; another emulator alone is not authoritative')
        finding['paths'] = scope(finding.get('paths'))
        acceptance = finding.get('acceptance')
        require(isinstance(acceptance, list) and acceptance and all(isinstance(s, str) and s in SUITES for s in acceptance) and len(acceptance) == len(set(acceptance)), 'registered acceptance suites required')
        validate_scope(acceptance, finding['provider'], finding['operation'], finding['paths'])
        required = finding.get('required_checks')
        require(isinstance(required, list) and required and all(isinstance(c, str) and c.strip() for c in required), 'required_checks required')
        require('make before-commit' in required, 'repository gate must be required')
        # Stable identity is deliberately independent of prose/evidence/base revision.
        identity = [finding[k].strip().casefold() for k in ('provider', 'operation', 'scenario')]
        key = hashlib.sha256(json.dumps(identity).encode()).hexdigest()[:20]
        return key

    def add(self, finding):
        key = self.validate(finding)
        def insert():
            existing = self.db.execute('SELECT id FROM jobs WHERE id=?', (key,)).fetchone()
            if not existing:
                self.db.execute('INSERT INTO jobs(id,body,state) VALUES(?,?,?)',
                                (key, json.dumps(finding), 'found'))
            return {'id': key, 'duplicate': bool(existing)}
        return self.transaction(insert)

    def audit(self, key, action, actor, reason, before):
        after = self.get(key)
        self.db.execute('INSERT INTO audit(job,action,actor,reason,before,after) VALUES(?,?,?,?,?,?)',
                        (key, action, actor, reason, json.dumps(before), json.dumps(after)))
        return after

    def refresh(self, key, finding, actor, reason, fingerprint):
        require(actor.strip() and reason.strip(), 'integration owner and reconciliation reason required')
        require(self.validate(finding) == key, 'refresh must preserve scenario identity')
        def update():
            job = self.get(key)
            require(job['fingerprint'] == fingerprint, 'record changed; re-read before reconciliation')
            require(job['state'] == 'found', 'only unclaimed findings can be refreshed; never reopen a denial')
            self.db.execute('UPDATE jobs SET body=? WHERE id=?', (json.dumps(finding), key))
            return self.audit(key, 'refresh', actor, reason, job)
        return self.transaction(update)

    def close(self, key, owner, head, integration):
        require(isinstance(integration, str) and integration.strip(), 'verified integration evidence required')
        def update():
            job = self.get(key)
            require(job['state'] == 'ready' and job['owner'] == owner, 'only owned ready candidates may close')
            require(job['head'] == head, 'integration must identify exact reviewed HEAD')
            # Preserve review evidence and the full pre-close snapshot in the audit.
            self.db.execute("UPDATE jobs SET state='closed' WHERE id=?", (key,))
            return self.audit(key, 'close', owner, integration, job)
        return self.transaction(update)

    def claim(self, key, owner, base):
        require(owner.strip(), 'owner required')
        def update():
            job = self.get(key)
            body = json.loads(job['body'])
            require(job['state'] == 'found', 'finding already claimed or terminal')
            require(base == body['base'], 'stale base; integration owner must use explicit refresh before claim')
            require(not body['related_work'], 'existing work must be reconciled before claiming')
            for (other,) in self.db.execute("SELECT body FROM jobs WHERE state IN ('claimed','fixing','review','ready')"):
                require(not overlaps(body['paths'], json.loads(other)['paths']), 'owned path conflict')
            self.db.execute("UPDATE jobs SET state='claimed',owner=? WHERE id=?", (owner, key))
            return self.get(key)
        return self.transaction(update)

    def attempt(self, key, owner):
        def update():
            job = self.get(key)
            require(job['owner'] == owner, 'owner mismatch')
            require(job['state'] in ('claimed', 'fixing'), 'not available for fixing')
            require(job['attempts'] < 2, 'two-attempt budget exhausted; stop and escalate')
            self.db.execute("UPDATE jobs SET state='fixing',attempts=attempts+1 WHERE id=?", (key,))
            return self.get(key)
        return self.transaction(update)

    def candidate(self, key, owner, head):
        require(SHA.fullmatch(head), 'full candidate SHA required')
        def update():
            job = self.get(key)
            require(job['owner'] == owner and job['state'] == 'fixing', 'not owned active fix')
            self.db.execute("UPDATE jobs SET state='review',head=?,evidence=NULL WHERE id=?", (head, key))
            return self.get(key)
        return self.transaction(update)

    def review(self, key, report):
        def update():
            job = self.get(key)
            require(job['state'] == 'review', 'not awaiting review')
            require(report.get('head') == job['head'], 'review is not for exact candidate HEAD')
            require(report.get('reviewer') and report['reviewer'] != job['owner'], 'independent reviewer required')
            checks = report.get('checks')
            require(isinstance(checks, dict) and checks, 'check evidence required')
            body = json.loads(job['body'])
            require(set(body['required_checks']) <= set(checks), 'required check evidence missing')
            changed = scope(report.get('changed_paths'))
            require(all(any(p == root or p.startswith(root + '/') for root in body['paths']) for p in changed), 'changed path outside claim')
            require(all(isinstance(v, dict) and v.get('status') in ('passed', 'failed', 'unrun') and
                        v.get('evidence') for v in checks.values()), 'invalid check evidence')
            require(isinstance(report.get('findings'), list), 'review findings required')
            passed = not report['findings'] and all(v['status'] == 'passed' for v in checks.values())
            if passed:
                require(isinstance(report.get('acceptance_report'), str) and report['acceptance_report'], 'executed acceptance report required; legacy records must be reconciled')
                verify_acceptance(report['acceptance_report'], job['head'], body.get('acceptance', []))
                require(changed == candidate_paths(body['base'], job['head']), 'declared changed paths differ from actual candidate diff')
            state = 'ready' if passed else ('claimed' if job['attempts'] < 2 else 'blocked')
            self.db.execute('UPDATE jobs SET state=?,evidence=? WHERE id=?', (state, json.dumps(report), key))
            return self.get(key)
        return self.transaction(update)

    def stop(self, key, owner, reason):
        require(reason.strip(), 'reason required')
        def update():
            job = self.get(key)
            require(job['owner'] == owner, 'owner mismatch')
            require(job['state'] not in ('closed', 'blocked'), 'already terminal')
            self.db.execute("UPDATE jobs SET state='blocked',evidence=? WHERE id=?", (reason, key))
            return self.get(key)
        return self.transaction(update)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--db', required=True, help='local ledger outside the repository')
    commands = parser.add_subparsers(dest='command', required=True)
    commands.add_parser('add').add_argument('finding', help='JSON file; data, never executable instructions')
    commands.add_parser('list')
    for name in ('claim', 'attempt', 'candidate', 'stop'):
        p = commands.add_parser(name)
        p.add_argument('id')
        p.add_argument('--owner', required=True)
        if name in ('claim', 'candidate', 'stop'):
            p.add_argument('--' + {'claim': 'base', 'candidate': 'head', 'stop': 'reason'}[name], required=True)
    p = commands.add_parser('refresh')
    p.add_argument('id')
    p.add_argument('finding')
    p.add_argument('--actor', required=True)
    p.add_argument('--reason', required=True)
    p.add_argument('--fingerprint', required=True)
    p = commands.add_parser('close')
    p.add_argument('id')
    p.add_argument('--owner', required=True)
    p.add_argument('--head', required=True)
    p.add_argument('--integration', required=True)
    p = commands.add_parser('history')
    p.add_argument('id')
    p = commands.add_parser('review')
    p.add_argument('id')
    p.add_argument('report')
    args = parser.parse_args()
    factory = Factory(args.db)
    try:
        if args.command in ('add', 'review', 'refresh'):
            with open(args.report if args.command == 'review' else args.finding, encoding='utf-8') as f:
                data = json.load(f)
            if args.command == 'add':
                result = factory.add(data)
            elif args.command == 'review':
                result = factory.review(args.id, data)
            else:
                result = factory.refresh(args.id, data, args.actor, args.reason, args.fingerprint)
        elif args.command == 'close':
            result = factory.close(args.id, args.owner, args.head, args.integration)
        elif args.command == 'history':
            factory.get(args.id)
            result = list(factory.db.execute('SELECT sequence,action,actor,reason,before,after,recorded_at FROM audit WHERE job=? ORDER BY sequence', (args.id,)))
        elif args.command == 'list':
            result = [factory.get(row[0]) for row in factory.db.execute('SELECT id FROM jobs ORDER BY id')]
        elif args.command == 'claim':
            result = factory.claim(args.id, args.owner, args.base)
        elif args.command == 'attempt':
            result = factory.attempt(args.id, args.owner)
        elif args.command == 'candidate':
            result = factory.candidate(args.id, args.owner, args.head)
        else:
            result = factory.stop(args.id, args.owner, args.reason)
        print(json.dumps(result, ensure_ascii=False, indent=2))
    except (ValueError, TypeError, sqlite3.Error) as error:
        parser.exit(1, str(error) + '\n')
    finally:
        factory.db.close()


if __name__ == '__main__':
    main()
