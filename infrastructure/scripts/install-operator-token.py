#!/usr/bin/env python3
"""Create the operator token once, or verify the existing one. Run as root on the host."""
import argparse
import base64
import os
from pathlib import Path
import pwd
import re
import secrets
import stat
import sys
import tempfile


def private_file(path, owner):
    before = path.lstat()
    if not stat.S_ISREG(before.st_mode) or stat.S_IMODE(before.st_mode) != 0o600 or before.st_uid != owner or before.st_nlink != 1:
        raise ValueError("token requires an owner-controlled regular 0600 file")
    descriptor = os.open(path, os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0))
    with os.fdopen(descriptor, "rb") as source:
        opened = os.fstat(source.fileno())
        if not os.path.samestat(before, opened):
            raise ValueError("token changed during read")
        body = source.read(46)
    if len(body) > 45 or not os.path.samestat(before, path.lstat()):
        raise ValueError("token size or replacement rejected")
    return body


def create_private(path, value, owner):
    temporary = None
    try:
        with tempfile.NamedTemporaryFile(dir=path.parent, mode="w", delete=False) as file:
            temporary = file.name
            os.fchmod(file.fileno(), 0o600)
            os.fchown(file.fileno(), *owner)
            file.write(value)
            file.flush()
            os.fsync(file.fileno())
        os.link(temporary, path)  # Never overwrite an existing operator identity.
        directory = os.open(path.parent, os.O_RDONLY)
        try:
            os.fsync(directory)
        finally:
            os.close(directory)
    finally:
        if temporary and os.path.exists(temporary):
            os.unlink(temporary)


def install_operator_token(directory=Path("/etc/wss-relay/secrets"), owner=None, filename="operator-token"):
    if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_.-]{0,127}", filename):
        raise ValueError("filename must be one safe basename")
    info = directory.lstat()
    if not stat.S_ISDIR(info.st_mode) or info.st_uid != os.geteuid() or info.st_mode & 0o022:
        raise ValueError("runtime directory must be owner-controlled without writable group/other permissions")
    if owner is None:
        entry = pwd.getpwnam("wss-relay")
        owner = (entry.pw_uid, entry.pw_gid)
    path = directory / filename
    if path.exists() or path.is_symlink():
        existing = private_file(path, owner[0])
        if existing.endswith(b"\r\n"):
            existing = existing[:-2]
        elif existing.endswith(b"\n"):
            existing = existing[:-1]
        if not re.fullmatch(rb"[A-Za-z0-9_-]{43}", existing):
            raise ValueError("existing operator token invalid; do not silently replace it")
        decoded = base64.urlsafe_b64decode(existing + b"=")
        if not any(decoded) or base64.urlsafe_b64encode(decoded).rstrip(b"=") != existing:
            raise ValueError("existing operator token is not canonical")
        return False
    # Same format as operatortoken.Parse: 32 nonzero bytes, raw base64url.
    value = bytes(32)
    while not any(value):
        value = secrets.token_bytes(32)
    create_private(path, base64.urlsafe_b64encode(value).rstrip(b"=").decode("ascii"), owner)
    return True


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--directory", type=Path, default=Path("/etc/wss-relay/secrets"))
    parser.add_argument("--service-user", default="wss-relay")
    parser.add_argument("--filename", default="operator-token")
    args = parser.parse_args()
    if os.geteuid() != 0:
        parser.error("run as root")
    os.umask(0o077)
    try:
        entry = pwd.getpwnam(args.service_user)
        created = install_operator_token(args.directory, (entry.pw_uid, entry.pw_gid), args.filename)
    except Exception:
        # Never print token material or raw exception details.
        print("Operator token installation failed; check /etc/wss-relay/secrets ownership and the existing token file.", file=sys.stderr)
        return 1
    print("Operator token created." if created else "Existing operator token verified and preserved.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
