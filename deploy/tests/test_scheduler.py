import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import time
import unittest


SCHEDULER = Path(__file__).resolve().parents[1] / "scheduler.py"


class SchedulerTest(unittest.TestCase):
    def test_failed_round_retries_and_shutdown_drains_the_next_round(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            fake_cli = root / "kindle-sync"
            fake_cli.write_text(f"#!{sys.executable}\n" + """
import json, sys, time
from pathlib import Path
assert sys.argv[1:] == ["sync", "--json"]
if not Path("attempted").exists():
    Path("attempted").touch()
    print(json.dumps({"actions": [], "problems": ["private-fixture-detail"]}))
    sys.exit(1)
Path("active").touch()
while not Path("release").exists():
    time.sleep(0.01)
print(json.dumps({"actions": ["submitted X to Gmail", "archived Y under X as Z"]}))
""")
            fake_cli.chmod(0o755)
            command = "import runpy,sys; runpy.run_path(sys.argv[1])['main'](0.05)"
            worker = subprocess.Popen(
                [sys.executable, "-I", "-c", command, str(SCHEDULER)],
                env=dict(os.environ, PATH=str(root) + os.pathsep + os.environ["PATH"]),
                cwd=root, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True,
            )
            try:
                deadline = time.monotonic() + 5
                while not (root / "active").exists():
                    self.assertIsNone(worker.poll())
                    self.assertLess(time.monotonic(), deadline)
                    time.sleep(0.01)
                worker.terminate()
                (root / "release").touch()
                stdout, stderr = worker.communicate(timeout=5)
            finally:
                if worker.poll() is None:
                    (root / "release").touch()
                    worker.kill()
                    worker.communicate()
            self.assertEqual(worker.returncode, 0)
            self.assertNotIn("private-fixture-detail", stdout + stderr)
            events = [json.loads(line) for line in stdout.splitlines()]
            self.assertEqual([e["event"] for e in events], ["sync_started", "sync_finished"] * 2)
            first, second = events[1], events[3]
            self.assertEqual((first["exit_code"], first["problems"]), (1, 1))
            self.assertEqual((second["exit_code"], second["submitted"], second["archived"]), (0, 1, 1))
