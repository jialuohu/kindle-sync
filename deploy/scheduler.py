"""Run one sync at startup and on each available five-minute tick."""

from datetime import datetime, timezone
import json
import math
import signal
import subprocess
import threading
import time


def timestamp():
    return datetime.now(timezone.utc).isoformat(timespec="seconds")


def main(interval=300):
    stopping = threading.Event()

    def stop(signum, frame):
        # A stopped scheduler drains its current sync, including any Gmail
        # submission already in flight, before relinquishing the state volume.
        stopping.set()

    signal.signal(signal.SIGTERM, stop)
    signal.signal(signal.SIGINT, stop)
    while not stopping.is_set():
        tick = time.monotonic()
        started = timestamp()
        print(json.dumps({"event": "sync_started", "started_at": started}), flush=True)
        # Reports and provider errors can contain paper keys, private paths
        # and Drive identifiers. Keep only round totals in the Docker log.
        result = subprocess.run(["kindle-sync", "sync", "--json"], capture_output=True)
        code = result.returncode
        try:
            report = json.loads(result.stdout)
        except ValueError:
            report = {}
            code = code or 1
        actions = report.get("actions") or []
        problems = report.get("problems") or []
        print(json.dumps({
            "event": "sync_finished", "started_at": started, "finished_at": timestamp(),
            "exit_code": code, "submitted": sum(a.startswith("submitted ") for a in actions),
            "archived": sum(a.startswith("archived ") for a in actions),
            "retired": sum(a.startswith("retired older notes: ") for a in actions),
            "problems": max(len(problems), int(code != 0)),
        }), flush=True)
        elapsed = time.monotonic() - tick
        next_tick = max(1, math.ceil(elapsed / interval)) * interval
        stopping.wait(next_tick - elapsed)


if __name__ == "__main__":
    main()
