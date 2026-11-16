"""Required, narrowly allowlisted seed-prefix replay; not a refinement proof."""
import hashlib
import json
from pathlib import Path
import subprocess
import re

SPECS = ({'model': 'NICSessionBookkeeping', 'harness': 'NICSessionSeedReachability', 'files': {'scripts/formal_support/NICSessionSeedReachability.tla': '4a8122deb47a3a9d27ee2fe8d1efa9485ff4e5cabdf7f9ce2b84d39321dd2d84', 'scripts/formal_support/NICSessionSeedReachability.cfg': '0d9ec7518b1b79b9a465ab34dcfbbdcf1eeee1accbd464e6868cc5fddaeb9c11', 'formal/NICSessionBookkeeping.tla': '9df2b999f750854d2a963fc2b5042314eab3f1fcdaf68fa402d5cab6e939e88a'}, 'old': 'Retired, ClosingRead>>', 'new': 'Retired, One>>'}, {'model': 'ProbeOperationLifecycle', 'harness': 'ProbeOperationSeedReachability', 'files': {'scripts/formal_support/ProbeOperationSeedReachability.tla': 'a78e3d2e8a41a6405a071186c5e645e07737e8959040cab5e73ed71721da8b6a', 'scripts/formal_support/ProbeOperationSeedReachability.cfg': '92dc7d1116c079bb879b1f4a59ac803e1baf0f925fdb93538d8fc95d4d8fc658', 'formal/ProbeOperationLifecycle.tla': '2cd62b6c21c8227e2ba4e381e7b1b2472c724439bc354d474c8786dbb681e03d'}, 'old': 'State = SeedTargets[chosen]', 'new': 'State = IF chosen = Len(Plans) THEN EmptyState ELSE SeedTargets[chosen]'})

def prepare_seed_replays(root, registry, snapshot, require):
    """Read immutable snapshot bytes when supplied; never fall back to live files."""
    require(tuple(s["model"] for s in SPECS) == ("NICSessionBookkeeping", "ProbeOperationLifecycle"),
            "required seed replay mapping changed")
    result = []
    for spec in SPECS:
        require(any(m["id"] == spec["model"] for m in registry["models"]),
                "required seed replay model missing: " + spec["model"])
        content = {}
        for path, expected in spec["files"].items():
            file = Path(root) / path
            require(file.is_file() and not file.is_symlink(), "missing/unsafe seed replay input: " + path)
            raw = snapshot["content"].get(path) if snapshot else file.read_bytes()
            require(isinstance(raw, bytes), "seed replay absent from immutable HEAD: " + path)
            require(hashlib.sha256(raw).hexdigest() == expected, "seed replay input changed: " + path)
            content[path] = raw
        source = content["scripts/formal_support/" + spec["harness"] + ".tla"].decode()
        config = content["scripts/formal_support/" + spec["harness"] + ".cfg"].decode()
        require("INVARIANT SeedTargetReached" in config.splitlines()
                and "INVARIANT PrefixHasNext" in config.splitlines(),
                "required seed replay properties missing")
        require(source.count(spec["old"]) == 1, "invalid seed replay control anchor")
        result.append((spec, content))
    require(len(result) == 2, "both seed replay obligations required")
    return result

def run_seed_replays(root, registry, snapshot, temp, pinned_jar, java, reports,
                     timeout, evidence, require, validate_result):
    for spec, content in prepare_seed_replays(root, registry, snapshot, require):
        for negative in (False, True):
            name = spec["harness"] + ("_InvalidTarget" if negative else "")
            case = Path(temp) / name
            case.mkdir()
            for path, raw in content.items():
                (case / Path(path).name).write_bytes(raw)
            source_path = case / (spec["harness"] + ".tla")
            if negative:
                changed = source_path.read_text().replace(spec["old"], spec["new"])
                changed, count = re.subn(r"\bMODULE\s+" + spec["harness"] + r"\b", "MODULE " + name, changed)
                require(count == 1, "seed replay control module rename failed")
                source_path.unlink()
                source_path = case / (name + ".tla")
                source_path.write_text(changed)
            config_path = case / (spec["harness"] + ".cfg")
            record = {"model": spec["model"], "case": name, "kind": "seed-prefix-replay",
                      "success": False, "expected_violation": "SeedTargetReached" if negative else None,
                      "input_sha256": {path: hashlib.sha256(raw).hexdigest() for path, raw in content.items()},
                      "model_sha256": hashlib.sha256(source_path.read_bytes()).hexdigest(),
                      "config_sha256": hashlib.sha256(config_path.read_bytes()).hexdigest()}
            evidence["runs"].append(record)
            command = [java, "-cp", str(pinned_jar), "-Xmx256m", "tlc2.TLC",
                       "-workers", "1", "-metadir", str(case / "states"),
                       "-config", str(config_path), str(source_path)]
            try:
                process = subprocess.run(command, cwd=case, capture_output=True,
                                         text=True, timeout=timeout, check=False)
            except (OSError, subprocess.TimeoutExpired) as error:
                record["exception_type"] = type(error).__name__
                for channel in ("stdout", "stderr"):
                    raw = getattr(error, channel, None)
                    raw = raw if isinstance(raw, bytes) else raw.encode() if isinstance(raw, str) else b""
                    (Path(reports) / (name + "." + channel + ".bin")).write_bytes(raw)
                require(False, "seed replay could not finish: " + name)
            output = process.stdout + process.stderr
            (Path(reports) / (name + ".txt")).write_text(output)
            record["exit_code"] = process.returncode
            validate_result(process.returncode, output, record["expected_violation"])
            if negative:
                require("State 2:" in output, "invalid-seed control must fail beyond initial state")
            record["success"] = True
