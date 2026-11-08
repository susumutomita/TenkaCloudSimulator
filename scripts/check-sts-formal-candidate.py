"""Standalone STS model diagnostic and guard-sensitivity check.

This is not the repository's formal gate. Set TLA_JAR to an existing local jar.
"""
import os
from pathlib import Path
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[1]
MUTANTS = (
    "SkipGeneration", "SkipExpiry", "SkipSignature", "SkipTrust",
    "SkipExternal", "SkipPermission", "SkipTenant", "SkipChain",
)


def main():
    jar = Path(os.environ["TLA_JAR"]).resolve(strict=True)
    source = (ROOT / "formal/STSSessionLifecycle.tla").read_text()
    config = (ROOT / "formal/STSSessionLifecycle.cfg").read_text()
    with tempfile.TemporaryDirectory(prefix="sts-formal-") as directory:
        work = Path(directory)
        (work / "STSSessionLifecycle.tla").write_text(source)
        for mutant in (None, *MUTANTS):
            candidate = config if mutant is None else config.replace(
                f"{mutant} = FALSE", f"{mutant} = TRUE"
            )
            (work / "STSSessionLifecycle.cfg").write_text(candidate)
            result = subprocess.run(
                ["java", "-Xmx512m", "-cp", str(jar), "tlc2.TLC", "-workers", "1",
                 "-deadlock", "-config", "STSSessionLifecycle.cfg", "STSSessionLifecycle"],
                cwd=work, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                timeout=180,
            )
            output = result.stdout
            if mutant is None:
                ok = result.returncode == 0 and "Model checking completed. No error has been found." in output
            else:
                guard = "IssueRequiresAllGuards" if mutant in {"SkipTrust", "SkipExternal", "SkipPermission", "SkipChain"} else "UseRequiresAllGuards"
                ok = result.returncode != 0 and f"Invariant {guard} is violated" in output
            if not ok:
                raise RuntimeError(f"Unexpected result for {mutant or 'baseline'}:\n{output}")
            print(f"{mutant or 'baseline'}: {'counterexample detected' if mutant else 'passed'}", flush=True)


if __name__ == "__main__":
    main()
