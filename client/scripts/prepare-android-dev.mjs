// Trust the loopback test CA only in this app's debug variant; release is unaffected.
import { readFile, mkdir, writeFile } from 'node:fs/promises'
const source = new URL('../../.cache/dev-tls/cert.pem', import.meta.url)
const target = new URL('../android/app/src/debug/', import.meta.url)
const certificate = await readFile(source)
await mkdir(new URL('res/raw/', target), { recursive: true })
await mkdir(new URL('res/xml/', target), { recursive: true })
await writeFile(new URL('res/raw/relay_dev_ca.pem', target), certificate)
await writeFile(new URL('res/xml/relay_debug_network.xml', target), `<?xml version="1.0" encoding="utf-8"?>
<network-security-config>
  <base-config cleartextTrafficPermitted="false"><trust-anchors><certificates src="system" /></trust-anchors></base-config>
  <debug-overrides><trust-anchors><certificates src="@raw/relay_dev_ca" /></trust-anchors></debug-overrides>
</network-security-config>\n`)
await writeFile(new URL('AndroidManifest.xml', target), `<?xml version="1.0" encoding="utf-8"?>
<manifest xmlns:android="http://schemas.android.com/apk/res/android" xmlns:tools="http://schemas.android.com/tools">
  <application android:networkSecurityConfig="@xml/relay_debug_network" tools:replace="android:networkSecurityConfig" />
</manifest>\n`)
console.log('Prepared app-local debug CA; no private key copied.')
