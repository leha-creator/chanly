#!/usr/bin/env python3
"""Provision a persistent, separate diagnostic DB without arming provider writes.

Run on the dev host as root, from a checkout containing deploy/postgres/*.sql.
No credentials are printed, and repeated runs retain passwords and journal data.
"""
import argparse
import json
import os
from pathlib import Path
import re
import secrets
import subprocess
import tempfile
from urllib.parse import quote
import uuid


def read_env(path):
    result = {}
    for line in path.read_text().splitlines():
        if line and not line.lstrip().startswith('#') and '=' in line:
            key, value = line.split('=', 1)
            result[key.strip()] = value.strip().strip('"').strip("'")
    return result


def run(args, source=None):
    result = subprocess.run(args, input=source, text=True, capture_output=True)
    if result.returncode:
        # stderr can contain a DSN or a SQL statement with a password.
        raise RuntimeError('server setup command failed; sensitive output withheld')
    return result.stdout.strip()


def private_write(path, value):
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, 'w') as stream:
        stream.write(value)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--stack-dir', type=Path, default=Path('/opt/maxim-dev'))
    parser.add_argument('--image', required=True, help='already verified application image')
    args = parser.parse_args()
    if os.geteuid() != 0:
        raise RuntimeError('run as root on the dev host')
    stack_dir = args.stack_dir.resolve()
    stack = read_env(stack_dir / 'stack.env')
    app = read_env(stack_dir / 'app.env')
    if stack.get('COMPOSE_PROJECT_NAME') != 'maxim-dev' or app.get('APP_ENV') != 'development':
        raise RuntimeError('only the maxim-dev development stack is supported')
    container = 'maxim-dev-postgres-1'
    database = 'maxim_rw_control'
    journal_role = 'maxim_rw_journal_app'
    sql_dir = Path(__file__).resolve().parent.parent / 'postgres'
    # Refuse a directory with incomplete inputs before creating any credentials.
    roles_sql = (sql_dir / 'rocketwork-roles.sql').read_text()
    grants_sql = (sql_dir / 'rocketwork-grants.sql').read_text()

    def psql(sql, db='postgres'):
        return run(['docker', 'exec', '-i', container, 'psql', '-X', '-v',
                    'ON_ERROR_STOP=1', '-At', '-U', stack['POSTGRES_USER'], '-d', db], sql)

    secret_dir = stack_dir / 'rocketwork-control'
    secret_dir.mkdir(mode=0o700, exist_ok=True)
    if secret_dir.is_symlink() or secret_dir.stat().st_mode & 0o077:
        raise RuntimeError('control directory must be private and not a symlink')
    credentials = secret_dir / 'credentials.json'
    if not credentials.exists():
        existing = psql("SELECT count(*) FROM pg_roles WHERE rolname IN ('maxim_rw_runner','maxim_rw_policy')")
        if existing != '0':
            raise RuntimeError('existing roles require their original credentials file; passwords will not be reset')
        private_write(credentials, json.dumps({
            'runner_password': secrets.token_hex(32),
            'policy_password': secrets.token_hex(32),
            'arming_id': str(uuid.uuid4()),
        }) + '\n')
    if credentials.is_symlink() or credentials.stat().st_mode & 0o077:
        raise RuntimeError('credentials file must have private permissions')
    auth = json.loads(credentials.read_text())
    for key in ('runner_password', 'policy_password'):
        if not re.fullmatch(r'[0-9a-f]{64}', auth[key]):
            raise RuntimeError('unexpected credential format')
    uuid.UUID(auth['arming_id'])
    psql("\\set runner_password '" + auth['runner_password'] + "'\n" +
         "\\set policy_password '" + auth['policy_password'] + "'\n" + roles_sql)
    psql("""SELECT 'CREATE ROLE maxim_rw_journal_app NOLOGIN NOINHERIT'
WHERE NOT EXISTS(SELECT 1 FROM pg_roles WHERE rolname='maxim_rw_journal_app') \\gexec
SELECT 'CREATE DATABASE maxim_rw_control'
WHERE NOT EXISTS(SELECT 1 FROM pg_database WHERE datname='maxim_rw_control') \\gexec
REVOKE CONNECT ON DATABASE maxim_rw_control FROM PUBLIC;
GRANT CONNECT ON DATABASE maxim_rw_control TO maxim_rw_runner,maxim_rw_policy;
""")
    owner_url = ('postgres://' + quote(stack['POSTGRES_USER'], safe='') + ':' +
                 quote(stack['POSTGRES_PASSWORD'], safe='') + '@postgres:5432/' + database + '?sslmode=disable')
    # Migration secrets exist only on the host, with a narrow container lifetime.
    fd, migration_path = tempfile.mkstemp(prefix='migrate-', suffix='.env', dir=secret_dir)
    try:
        with os.fdopen(fd, 'w') as stream:
            stream.write('APP_ENV=development\nDB_APP_ROLE=' + journal_role + '\nDATABASE_URL=' + owner_url + '\n')
        network = run(['docker', 'inspect', container, '--format',
                       '{{range $name, $v := .NetworkSettings.Networks}}{{$name}}{{end}}'])
        if network != 'maxim-dev_backend':
            raise RuntimeError('unexpected dev PostgreSQL network')
        run(['docker', 'run', '--rm', '--network', network, '--read-only', '--tmpfs', '/tmp',
             '--cap-drop', 'ALL', '--security-opt', 'no-new-privileges:true',
             '--env-file', migration_path, args.image, 'migrate', 'up'])
    finally:
        Path(migration_path).unlink(missing_ok=True)
    psql("\\set app_role '" + journal_role + "'\n" + grants_sql, database)
    for kind in ('runner', 'policy'):
        path = stack_dir / ('rocketwork-' + kind + '.env')
        if path.exists():
            if path.is_symlink() or path.stat().st_mode & 0o077:
                raise RuntimeError('ops env must be a private regular file')
            # Preserve operator token/arming/DSN. Validate the existing target DB.
            config = read_env(path)
            key = 'ROCKETWORK_' + ('CONTROL' if kind == 'runner' else 'POLICY') + '_DATABASE_URL'
            if config.get(key) != ('postgres://maxim_rw_' + kind + ':' + auth[kind + '_password'] +
                                   '@postgres:5432/' + database + '?sslmode=disable'):
                raise RuntimeError('existing ops DSN differs; no file was overwritten')
            continue
        key = 'ROCKETWORK_' + ('CONTROL' if kind == 'runner' else 'POLICY') + '_DATABASE_URL'
        content = ('LOG_LEVEL=info\n' + key + '=postgres://maxim_rw_' + kind + ':' +
                   auth[kind + '_password'] + '@postgres:5432/' + database + '?sslmode=disable\n' +
                   'ROCKETWORK_CHECK_ARMING_ID=' + auth['arming_id'] + '\n')
        if kind == 'runner':
            content += 'ROCKETWORK_TARGET=production\nROCKETWORK_BASE_URL=https://api.rocketwork.ru\nROCKETWORK_TOKEN=\n'
        private_write(path, content)
    (stack_dir / 'rocketwork-input').mkdir(mode=0o700, exist_ok=True)
    print('Control DB and isolated roles ready; existing credentials and history retained.')
    print('No provider token installed, account armed, grant issued or payment executed.')


if __name__ == '__main__':
    try:
        main()
    except (RuntimeError, KeyError, ValueError, OSError):
        raise SystemExit('RocketWork setup failed; secret values were not printed.') from None
