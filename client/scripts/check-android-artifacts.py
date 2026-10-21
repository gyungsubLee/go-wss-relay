#!/usr/bin/env python3
"""Verify packaged debug artifacts against the exact built web bundle and Android tools."""
import hashlib
import json
import os
from pathlib import Path
import subprocess
import zipfile

root = Path(__file__).resolve().parents[2]
output = root / '_Build/android/2026-10-22-debug'
sdk = Path(os.environ['ANDROID_HOME'])
java = Path(os.environ['JAVA_HOME']) / 'bin'
tools = sdk / 'build-tools' / os.environ.get('ANDROID_BUILD_TOOLS', '34.0.0')
bundletool = Path(os.environ['BUNDLETOOL_JAR'])
web = root / '_Build/dist-app'
expected = {p.relative_to(web).as_posix(): p.read_bytes() for p in web.rglob('*') if p.is_file()}
assert expected and 'index.html' in expected
reports = []
for suffix, prefix in [('apk', 'assets/public/'), ('aab', 'base/assets/public/')]:
    artifact = output / f'RelayLab-debug.{suffix}'
    with zipfile.ZipFile(artifact) as archive:
        actual = {name.removeprefix(prefix): archive.read(name) for name in archive.namelist()
                  if name.startswith(prefix) and not name.endswith('/')}
        synced = root / 'client/android/app/src/main/assets/public'
        packaged = {p.relative_to(synced).as_posix(): p.read_bytes()
                    for p in synced.rglob('*') if p.is_file()}
        assert actual == packaged, f'{suffix}: packaged assets differ from Capacitor sync'
        assert all(actual.get(name) == data for name, data in expected.items()), f'{suffix}: web bundle differs'
        assert actual.keys() - expected.keys() <= {'cordova.js', 'cordova_plugins.js'}
        for name in archive.namelist():
            assert not name.endswith(('key.pem', '.keystore', '.jks', '.p12')), name
        assert not any(b'-----BEGIN PRIVATE KEY-----' in data for data in actual.values())
    if suffix == 'apk':
        subprocess.run([str(tools / 'apksigner'), 'verify', str(artifact)], check=True, capture_output=True)
        badging = subprocess.check_output([str(tools / 'aapt'), 'dump', 'badging', str(artifact)], text=True)
        assert "name='games.storio.relaydemo'" in badging
        assert "versionCode='1'" in badging and "versionName='1.0'" in badging
    else:
        verification = subprocess.run([str(java / 'jarsigner'), '-verify', str(artifact)],
                                      check=True, capture_output=True, text=True)
        assert 'jar verified.' in verification.stdout, 'unsigned AAB must not pass'
        subprocess.run([str(java / 'java'), '-jar', str(bundletool), 'validate',
                        '--bundle=' + str(artifact)], check=True, capture_output=True)
    reports.append({'file': str(artifact.relative_to(root)), 'bytes': artifact.stat().st_size,
                    'sha256': hashlib.sha256(artifact.read_bytes()).hexdigest(),
                    'variant': 'debug', 'applicationId': 'games.storio.relaydemo',
                    'versionCode': 1, 'versionName': '1.0',
                    'webAssets': 'all _Build/dist-app files match; Capacitor Cordova files also match synced assets',
                    'signing': 'debug signature verified; not a Play release upload artifact',
                    'deviceValidation': 'not run: no USB device attached'})
(root / 'docs/evidence/v2/2026-10-22-android-artifacts.json').write_text(json.dumps(reports, indent=2) + '\n')
print('PASS: APK/AAB signatures, Android structure, exact web bundle, no packaged private key')
