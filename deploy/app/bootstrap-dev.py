#!/usr/bin/env python3
"""Provision dev on the existing production host; run as root from a checkout.

Usage: python3 deploy/app/bootstrap-dev.py <published-postgres-image>
Requires the production MinIO stack to be running. Never copies production data.
"""
import base64
import json
import os
from pathlib import Path
import pwd
import secrets
import shutil
import subprocess
import sys


def read_env(path):
    return dict(line.split('=', 1) for line in path.read_text().splitlines()
                if line and not line.startswith('#') and '=' in line)


def save_env(path, values, owner):
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    os.fchown(fd, owner.pw_uid, owner.pw_gid)
    with os.fdopen(fd, 'w') as stream:
        stream.write(''.join(f'{key}={value}\n' for key, value in values.items()))


def main():
    if os.geteuid() != 0 or len(sys.argv) != 2:
        raise SystemExit('Run as root with the published PostgreSQL image argument')
    source = Path(__file__).resolve().parent
    dest = Path('/opt/maxim-dev')
    owner = pwd.getpwnam('deploy')
    dest.mkdir(mode=0o750, exist_ok=True)
    os.chown(dest, owner.pw_uid, owner.pw_gid)
    paths = [dest / 'app.env', dest / 'stack.env']
    if any(p.exists() for p in paths) and not all(p.exists() for p in paths):
        raise SystemExit('Incomplete dev configuration; review app.env and stack.env')
    if not paths[0].exists():
        token = lambda: secrets.token_hex(32)
        app = read_env(source / 'env/dev/app.env.example')
        stack = read_env(source / 'env/dev/stack.env.example')
        db_owner, db_app, db_backup = token(), token(), token()
        app.update(DATABASE_URL=f'postgres://maxim_app:{db_app}@postgres:5432/maxim?sslmode=disable',
                   APP_SECRETS_KEY=base64.b64encode(secrets.token_bytes(32)).decode(),
                   AUTH_CODE_PEPPER=token(), MAX_WEBHOOK_PATH_SECRET=token(),
                   MAX_WEBHOOK_SECRET=token(), S3_ACCESS_KEY='dev-' + secrets.token_hex(8),
                   S3_SECRET_KEY=token())
        stack.update(POSTGRES_PASSWORD=db_owner, POSTGRES_APP_PASSWORD=db_app,
                     POSTGRES_BACKUP_PASSWORD=db_backup, MAXIM_POSTGRES_IMAGE=sys.argv[1],
                     MIGRATE_DATABASE_URL=f'postgres://maxim:{db_owner}@postgres:5432/maxim?sslmode=disable',
                     BACKUP_S3_ACCESS_KEY='backup-dev-' + secrets.token_hex(6),
                     BACKUP_S3_SECRET_KEY=token())
        save_env(paths[0], app, owner)
        save_env(paths[1], stack, owner)
    app, stack = map(read_env, paths)
    subprocess.run([sys.executable, str(source / 'setup-dev-auth.py')], check=True)
    for name in ['compose.yml', 'maxim-app.caddy', 'maxim-dev.caddy', 'release.sh']:
        shutil.copyfile(source / name, dest / name)
    shutil.copyfile(source / 'compose.dev.yml', dest / 'compose.override.yml')
    shutil.copyfile(source.parent / 'scripts/lib.sh', dest / 'lib.sh')
    for name in ['compose.yml', 'maxim-app.caddy', 'maxim-dev.caddy', 'release.sh', 'compose.override.yml', 'lib.sh']:
        path = dest / name
        path.write_bytes(path.read_bytes().replace(b'\r\n', b'\n'))
        os.chown(path, owner.pw_uid, owner.pw_gid)
        os.chmod(path, 0o750 if name.endswith('.sh') else 0o640)
    root = read_env(Path('/opt/maxim-production/storage.env'))
    buckets = ['maxim-dev-private', 'maxim-dev-public']

    def policy(names):
        return json.dumps({'Version': '2012-10-17', 'Statement': [
            {'Effect': 'Allow', 'Action': ['s3:GetBucketLocation', 's3:ListBucket', 's3:ListBucketMultipartUploads'],
             'Resource': ['arn:aws:s3:::' + b for b in names]},
            {'Effect': 'Allow', 'Action': ['s3:GetObject', 's3:PutObject', 's3:DeleteObject',
                                         's3:AbortMultipartUpload', 's3:ListMultipartUploadParts'],
             'Resource': ['arn:aws:s3:::' + b + '/*' for b in names]}]})

    env = dict(os.environ, MINIO_ROOT_USER=root['MINIO_ROOT_USER'],
               MINIO_ROOT_PASSWORD=root['MINIO_ROOT_PASSWORD'],
               APP_KEY=app['S3_ACCESS_KEY'], APP_SECRET=app['S3_SECRET_KEY'],
               BACKUP_KEY=stack['BACKUP_S3_ACCESS_KEY'], BACKUP_SECRET=stack['BACKUP_S3_SECRET_KEY'],
               APP_POLICY=policy(buckets), BACKUP_POLICY=policy(['maxim-dev-backups']))
    script = r'''
set -eu
mc alias set root http://maxim-production-storage:9000 "$MINIO_ROOT_USER" "$MINIO_ROOT_PASSWORD" --api S3v4 >/dev/null
mc mb --ignore-existing root/maxim-dev-private root/maxim-dev-public root/maxim-dev-backups
mc anonymous set none root/maxim-dev-private
mc anonymous set none root/maxim-dev-backups
printf '%s' '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":["*"]},"Action":["s3:GetObject"],"Resource":["arn:aws:s3:::maxim-dev-public/*"]}]}' >/tmp/public.json
mc anonymous set-json /tmp/public.json root/maxim-dev-public
printf '%s' "$APP_POLICY" >/tmp/app.json
printf '%s' "$BACKUP_POLICY" >/tmp/backup.json
mc admin user add root "$APP_KEY" "$APP_SECRET" >/dev/null
mc admin user add root "$BACKUP_KEY" "$BACKUP_SECRET" >/dev/null
mc admin policy create root maxim-dev-app /tmp/app.json >/dev/null
mc admin policy create root maxim-dev-backup /tmp/backup.json >/dev/null
mc admin policy attach root maxim-dev-app --user "$APP_KEY" >/dev/null
mc admin policy attach root maxim-dev-backup --user "$BACKUP_KEY" >/dev/null
'''
    args = ['docker', 'run', '--rm', '--network', 'edge', '--tmpfs', '/tmp', '-e', 'MC_CONFIG_DIR=/tmp/mc']
    for name in ['MINIO_ROOT_USER', 'MINIO_ROOT_PASSWORD', 'APP_KEY', 'APP_SECRET',
                 'BACKUP_KEY', 'BACKUP_SECRET', 'APP_POLICY', 'BACKUP_POLICY']:
        args.extend(['-e', name])
    args.extend(['--entrypoint', '/bin/sh', 'pgsty/mc:RELEASE.2026-09-16T00-00-00Z', '-c', script])
    subprocess.run(args, env=env, check=True)
    print('Dev configuration and isolated storage provisioned')


if __name__ == '__main__':
    main()
