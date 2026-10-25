"""Record assertion outcomes without serializing Ansible results or secrets."""
import json
import os
import time
from pathlib import Path

from ansible.plugins.callback import CallbackBase


class CallbackModule(CallbackBase):
    CALLBACK_VERSION = 2.0
    CALLBACK_TYPE = "aggregate"
    CALLBACK_NAME = "timings"
    CALLBACK_NEEDS_ENABLED = True

    def __init__(self):
        super().__init__()
        self.started = {}

    def v2_playbook_on_task_start(self, task, is_conditional):
        self.started[str(task._uuid)] = time.monotonic()

    def record(self, result, status):
        target = os.environ.get("KUBERPACK_TEST_ARTIFACTS")
        if not target:
            return
        task = result._task
        event = {"id": str(task._uuid), "name": task.get_name(), "status": status,
                 "duration": round(time.monotonic() - self.started.get(str(task._uuid), time.monotonic()), 4)}
        # Even no_log task names are safe: this callback never reads task args,
        # stdout, stderr, variables or result payloads.
        with (Path(target) / "ansible-events.jsonl").open("a") as stream:
            stream.write(json.dumps(event) + "\n")

    def v2_runner_on_ok(self, result):
        self.record(result, "passed")

    def v2_runner_on_failed(self, result, ignore_errors=False):
        self.record(result, "failed")

    def v2_runner_on_unreachable(self, result):
        self.record(result, "failed")

    def v2_runner_on_skipped(self, result):
        self.record(result, "skipped")
