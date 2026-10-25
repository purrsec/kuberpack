"""Fail and sanitize artifacts if a credential escaped the normal log paths."""
import json
from pathlib import Path
import sys

from privacy import collect_values, redact


def main():
    artifacts = Path(sys.argv[1])
    values, inventory_complete = collect_values()
    changed = []
    for path in artifacts.rglob("*"):
        if not path.is_file():
            continue
        try:
            original = path.read_text()
        except UnicodeError:
            continue
        clean = redact(original, values)
        if original != clean:
            path.write_text(clean)
            changed.append(str(path.relative_to(artifacts)))
    result = dict(inventory_complete=inventory_complete, unexpected_credential_files=changed)
    (artifacts / "artifact-privacy.json").write_text(json.dumps(result, indent=2) + "\n")
    if changed or not inventory_complete:
        print("Artifact privacy assertion failed; exposed values were removed.", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
