#!/usr/bin/env python3
"""Generate server-only dev Basic Auth files; does not publish the Mailpit site."""
import json
import os
from pathlib import Path
import pwd
import secrets
import subprocess
import tempfile


def write_private(path, data, uid, gid, mode=0o600):
    fd, temp = tempfile.mkstemp(prefix='.auth-', dir=path.parent)
    try:
        os.fchmod(fd, mode)
        os.fchown(fd, uid, gid)
        with os.fdopen(fd, 'w') as stream:
            stream.write(data)
        os.replace(temp, path)
    finally:
        if os.path.exists(temp):
            os.unlink(temp)


def main():
    if os.geteuid() != 0:
        raise SystemExit('Run as root')
    owner = pwd.getpwnam('deploy')
    credentials = Path('/opt/maxim-dev/basic-auth.json')
    if credentials.exists():
        values = json.loads(credentials.read_text())
    else:
        values = {'username': 'dev', 'password': secrets.token_urlsafe(24)}
        write_private(credentials, json.dumps(values) + '\n', 0, 0)
    username, password = values['username'], values['password']
    if not username.isalnum() or not password or any(c in password for c in '\n\r"\\:'):
        raise SystemExit('Use an alphanumeric username and password without quotes, colon or backslash')
    result = subprocess.run(['docker', 'exec', '-i', 'maxim-edge-caddy-1',
                             'caddy', 'hash-password'], input=password + '\n', text=True,
                            capture_output=True, check=True)
    hashed = result.stdout.strip()
    if not hashed.startswith('$2'):
        raise SystemExit('Unexpected password hash output')
    auth = Path('/opt/edge/auth')
    auth.mkdir(mode=0o750, exist_ok=True)
    os.chown(auth, owner.pw_uid, owner.pw_gid)
    write_private(auth / 'dev.caddy', f'basic_auth {{\n    {username} {hashed}\n}}\n',
                  owner.pw_uid, owner.pw_gid, 0o640)
    write_private(Path('/opt/maxim-dev/basic-auth.curl'), f'user = "{username}:{password}"\n',
                  owner.pw_uid, owner.pw_gid)
    print('Basic Auth files prepared; credentials remain in /opt/maxim-dev/basic-auth.json')


if __name__ == '__main__':
    main()
