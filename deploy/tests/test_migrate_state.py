import fcntl
import json
from pathlib import Path
import sqlite3
import subprocess
import sys
import tempfile
import unittest


SCRIPT = Path(__file__).resolve().parents[1] / "migrate_state.py"


class MigrationTest(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        root = Path(temp.name).resolve()
        self.source, self.snapshot, self.target = (root / n for n in ("source", "backup", "restored"))
        self.source.mkdir()
        db = sqlite3.connect(self.source / "sync.sqlite")
        self.addCleanup(db.close)
        db.execute("PRAGMA journal_mode=WAL")
        db.executescript("""
            CREATE TABLE meta(k TEXT PRIMARY KEY,v TEXT);
            CREATE TABLE jobs(id TEXT PRIMARY KEY,data TEXT);
            CREATE TABLE receipts(id TEXT,version TEXT,data TEXT,PRIMARY KEY(id,version));
            INSERT INTO meta VALUES ('identity','example'),('baseline','1'),('drive_folder','folder');
            INSERT INTO receipts VALUES ('export','2','{"state":"archived","child":"CHILD001"}');
        """)
        manual = self.source / "spool/manual/example.pdf"
        manual.parent.mkdir(parents=True)
        manual.write_bytes(b"synthetic PDF contents")
        db.execute("INSERT INTO jobs VALUES (?,?)", ("PAPER001", json.dumps(
            {"state": "manual_upload", "manual_file": str(manual)})))
        db.commit()

    def run_script(self, *args, ok=True):
        result = subprocess.run([sys.executable, str(SCRIPT), *args], capture_output=True, text=True)
        self.assertEqual(result.returncode == 0, ok, result.stderr)
        return result

    def test_round_trip_and_retry_after_spool_was_published(self):
        self.assertEqual(json.loads(self.run_script("backup", self.source, self.snapshot).stdout),
                         {"jobs": 1, "receipts": 1})
        cached = self.target / "spool/manual/example.pdf"
        cached.parent.mkdir(parents=True)
        cached.write_bytes(b"unrelated content")
        self.run_script("restore", self.snapshot, self.target, ok=False)
        self.assertFalse((self.target / "sync.sqlite").exists())
        cached.write_bytes(b"synthetic PDF contents")
        self.run_script("restore", self.snapshot, self.target)
        with sqlite3.connect(self.target / "sync.sqlite") as db:
            job = json.loads(db.execute("SELECT data FROM jobs").fetchone()[0])
            self.assertEqual(job["manual_file"], str(cached))
            self.assertEqual(cached.read_bytes(), b"synthetic PDF contents")
            self.assertEqual(db.execute("SELECT v FROM meta WHERE k='drive_folder'").fetchone(), ("folder",))
            self.assertEqual(db.execute("SELECT version FROM receipts").fetchone(), ("2",))
        self.run_script("restore", self.snapshot, self.target, ok=False)

    def test_locked_source_and_unexpected_symlink(self):
        with (self.source / "sync.lock").open("wb") as handle:
            fcntl.flock(handle.fileno(), fcntl.LOCK_EX)
            self.run_script("backup", self.source, self.snapshot, ok=False)
        self.assertFalse(self.snapshot.exists())
        (self.source / "spool" / "external").symlink_to(self.source / "sync.sqlite")
        self.run_script("backup", self.source, self.snapshot, ok=False)
        self.assertFalse(self.snapshot.exists())


if __name__ == "__main__":
    unittest.main()
