"""Make infrastructure failures visible even when a CI toolbox never starts."""
import importlib.util
from pathlib import Path
import sys


def main():
    artifacts = Path(sys.argv[1])
    artifacts.mkdir(parents=True, exist_ok=True)
    if (artifacts / "result.json").exists() and (artifacts / "junit.xml").exists():
        return
    spec = importlib.util.spec_from_file_location("test_launcher", Path(__file__).resolve().parents[1] / "run.py")
    launcher = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(launcher)
    launcher.record_failure(artifacts, "test runner startup", "No complete test report was produced. Inspect the CI preparation or runner step.")


if __name__ == "__main__":
    main()
