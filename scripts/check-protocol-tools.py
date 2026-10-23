#!/usr/bin/env python3
"""Exercise generation drift and failure safety in an isolated Git copy."""

import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile


root = Path(__file__).resolve().parent.parent
go_output = Path("gen/go/relay/v2/relay.pb.go")
ts_output = Path("client/src/relay/gen/relay/v2/relay_pb.ts")
baseline = Path("api/relay/v2/relay-baseline.binpb")
environment = dict(os.environ, GOTOOLCHAIN="local")
environment["GOCACHE"] = str(root / ".cache/go-build")
environment["GOMODCACHE"] = str(root / ".cache/go-mod")


with tempfile.TemporaryDirectory(prefix="relay-protocol-") as directory:
    work = Path(directory)

    def run(*command, succeeds=True):
        print("+", " ".join(command), flush=True)
        result = subprocess.run(command, cwd=work, env=environment)
        print(f"exit_status={result.returncode}", flush=True)
        assert (result.returncode == 0) == succeeds, command

    paths = subprocess.check_output(["git", "ls-files", "-z"], cwd=root).split(b"\0")
    for entry in paths:
        source = root / os.fsdecode(entry)
        if entry and source.is_file():
            destination = work / source.relative_to(root)
            destination.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(source, destination)
    # Include a new helper before its first commit, as well as tracked scripts.
    shutil.copytree(root / "scripts", work / "scripts", dirs_exist_ok=True)
    (work / ".tools").symlink_to(root / ".tools", target_is_directory=True)
    (work / ".cache").symlink_to(root / ".cache", target_is_directory=True)
    (work / "client/node_modules").symlink_to(root / "client/node_modules", target_is_directory=True)
    run("git", "init", "-q")
    run("git", "config", "core.autocrlf", "false")
    run("git", "add", ".")
    run("git", "-c", "user.name=Protocol check", "-c", "user.email=protocol@example.invalid",
        "commit", "-qm", "Isolated protocol inputs")
    original = {
        path: (work / path).read_bytes()
        for path in (go_output, ts_output, baseline)
    }
    schema = work / "api/relay/v2/relay.proto"
    original_schema = schema.read_text()
    schema.write_text(original_schema.replace(
        "message Connect {", "message Connect {\n  uint64 tooling_probe = 100;"
    ))
    go_template = json.dumps({
        "version": "v2",
        "plugins": [{
            "local": ".tools/bin/protoc-gen-go",
            "out": "gen/go",
            "opt": ["paths=source_relative"],
        }],
    })
    run(".tools/bin/buf", "generate", "--template", go_template)
    # A committed source + Go update must still fail for the stale TS output.
    run("git", "add", "api/relay/v2/relay.proto", str(go_output))
    run("git", "-c", "user.name=Protocol check", "-c", "user.email=protocol@example.invalid",
        "commit", "-qm", "Schema and Go only")
    updated_go = (work / go_output).read_bytes()
    run("make", "protocol-check", succeeds=False)
    assert (work / go_output).read_bytes() == updated_go
    assert (work / ts_output).read_bytes() == original[ts_output]
    assert (work / baseline).read_bytes() == original[baseline]
    print("PASS: schema + Go update rejects stale TS without changing outputs", flush=True)

    run(".tools/bin/protoc", "--plugin=protoc-gen-es=client/node_modules/.bin/protoc-gen-es",
        "--es_out=client/src/relay/gen", "--es_opt=target=ts", "-I", "api",
        "api/relay/v2/relay.proto")
    updated_ts = (work / ts_output).read_bytes()
    (work / go_output).write_bytes(original[go_output])
    run("git", "add", str(go_output), str(ts_output))
    run("git", "-c", "user.name=Protocol check", "-c", "user.email=protocol@example.invalid",
        "commit", "-qm", "Schema and TS only")
    run("make", "protocol-check", succeeds=False)
    assert (work / go_output).read_bytes() == original[go_output]
    assert (work / ts_output).read_bytes() == updated_ts
    print("PASS: schema + TS update rejects stale Go without changing outputs", flush=True)

    (work / go_output).write_bytes(updated_go)
    run("make", "protocol-check")
    print("PASS: updated Go and TS pass with a dirty working tree", flush=True)
    schema.write_text(schema.read_text().replace(
        "message Connect {", "message Connect {\n  uint64 second_tooling_probe = 101;"))
    run("make", "protocol-check", succeeds=False)
    assert (work / go_output).read_bytes() == updated_go
    assert (work / ts_output).read_bytes() == updated_ts
    print("PASS: schema-only addition rejects stale outputs without changing them", flush=True)
    run("make", "proto-generate")
    run("make", "protocol-check")
    assert (work / go_output).read_bytes() != updated_go
    assert (work / ts_output).read_bytes() != updated_ts
    print("PASS: generation updates both languages and restored gate passes", flush=True)

    before_failure = {path: (work / path).read_bytes() for path in original}
    schema.write_text(schema.read_text().replace(
        "message Connect {", "message Connect {\n  uint64 third_tooling_probe = 102;"))
    template = work / "buf.gen.yaml"
    original_template = template.read_text()
    template.write_text(original_template.replace(
        "client/node_modules/.bin/protoc-gen-es", "client/node_modules/.bin/missing-protoc-gen-es"))
    run("make", "proto-generate", succeeds=False)
    assert all((work / path).read_bytes() == content for path, content in before_failure.items())
    print("PASS: TS generator failure preserves both tracked outputs and baseline", flush=True)

    template.write_text(original_template)
    schema.write_text(original_schema)
    for path, content in original.items():
        (work / path).write_bytes(content)
    run("make", "proto-baseline", succeeds=False)
    assert (work / baseline).read_bytes() == original[baseline]
    run("make", "protocol-check")
    print("PASS: original schema/outputs restored; immutable baseline unchanged", flush=True)
