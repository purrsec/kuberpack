"""Import Docker images into the disposable node, checking both pipe ends."""
import argparse
import subprocess
import sys


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--node", required=True)
    parser.add_argument("images", nargs="+")
    args = parser.parse_args()
    save = subprocess.Popen(["docker", "image", "save", *args.images], stdout=subprocess.PIPE)
    load = None
    try:
        load = subprocess.Popen(["docker", "exec", "-i", args.node, "/bin/ctr", "--namespace=k8s.io", "images", "import", "-"], stdin=save.stdout)
        save.stdout.close()
        load_code = load.wait(timeout=180)
        save_code = save.wait(timeout=30)
        if save_code or load_code:
            raise RuntimeError(f"image transfer failed: docker-save={save_code}, containerd-import={load_code}")
    finally:
        for process in (load, save):
            if process is not None and process.poll() is None:
                process.kill()
                process.wait(timeout=15)


if __name__ == "__main__":
    main()
