import { execFileSync } from 'node:child_process'
import { fromBinary, toBinary } from '@bufbuild/protobuf'
import { ClientEnvelopeSchema, ServerEnvelopeSchema } from '../src/relay/gen/relay/v2/relay_pb.ts'

const binary = new URL('../../out/wire-check', import.meta.url).pathname
const fixtures: { name: string; kind: string; bytes: string }[] = JSON.parse(execFileSync(binary, { encoding: 'utf8' }))
for (const fixture of fixtures) {
  const bytes = Buffer.from(fixture.bytes, 'base64')
  fixture.bytes = Buffer.from(fixture.kind === 'ClientEnvelope'
    ? toBinary(ClientEnvelopeSchema, fromBinary(ClientEnvelopeSchema, bytes))
    : toBinary(ServerEnvelopeSchema, fromBinary(ServerEnvelopeSchema, bytes))).toString('base64')
}
process.stdout.write(execFileSync(binary, ['-verify'], { input: JSON.stringify(fixtures), encoding: 'utf8' }))
