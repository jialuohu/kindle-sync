"""Back up Kindle Sync's SQLite database and PDF spool, or restore an offline snapshot."""

import argparse
from contextlib import closing, contextmanager
import fcntl
import filecmp
import json
import os
from pathlib import Path
import shutil
import sqlite3
import sys
import tempfile


@contextmanager
def lock_writer(directory):
    with (directory / "sync.lock").open("a+b") as handle:
        try:
            fcntl.flock(handle, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError as exc:
            raise ValueError("Another sync owns the state lock. Wait for it to finish.") from exc
        yield


def copy_database(source, target):
    if source.is_symlink() or not source.is_file():
        raise ValueError("The source database is missing or is not a regular file.")
    with closing(sqlite3.connect(source.as_uri() + "?mode=ro", uri=True)) as original:
        with closing(sqlite3.connect(target)) as copied:
            original.backup(copied)
            copied.execute("PRAGMA journal_mode=DELETE")
            if copied.execute("PRAGMA integrity_check").fetchall() != [("ok",)]:
                raise ValueError("Database integrity check failed.")
            if not copied.execute("SELECT 1 FROM meta WHERE k='identity'").fetchone():
                raise ValueError("This database has no Kindle Sync identity.")
            return {table: copied.execute(f"SELECT COUNT(*) FROM {table}").fetchone()[0]
                    for table in ("jobs", "receipts")}


def spool_files(root):
    if root.is_symlink():
        raise ValueError("The migration does not follow spool symlinks.")
    files = set()
    for path in root.rglob("*"):
        if path.is_symlink() or not (path.is_dir() or path.is_file()):
            raise ValueError("Spool contains a symlink or a special file.")
        if path.is_file():
            files.add(path.relative_to(root))
    return files


def check_copy(source, copy, files):
    if spool_files(copy) != files or any(
        not filecmp.cmp(source / name, copy / name, shallow=False) for name in files
    ):
        raise ValueError("Spool contents do not match the snapshot.")


def stage_copy(source, stage):
    counts = copy_database(source / "sync.sqlite", stage / "sync.sqlite")
    source_spool, copy_spool = source / "spool", stage / "spool"
    if source_spool.exists() or source_spool.is_symlink():
        files = spool_files(source_spool)
        shutil.copytree(source_spool, copy_spool)
        check_copy(source_spool, copy_spool, files)
    return counts


def backup(source, destination):
    if destination.exists():
        raise ValueError("Choose a new path for the backup.")
    with lock_writer(source), tempfile.TemporaryDirectory(dir=destination.parent) as temp:
        stage = Path(temp) / "snapshot"
        stage.mkdir(mode=0o700)
        counts = stage_copy(source, stage)
        if destination.exists():
            raise ValueError("Choose a new path for the backup.")
        stage.rename(destination)
    return counts


def relocate_manual_files(stage, destination):
    with closing(sqlite3.connect(stage / "sync.sqlite")) as db, db:
        for key, data in db.execute("SELECT id, data FROM jobs").fetchall():
            job = json.loads(data)
            if not job.get("manual_file"):
                continue
            old = Path(job["manual_file"])
            if old.parts[-3:-1] != ("spool", "manual") or old.name in ("", ".", ".."):
                raise ValueError("Invalid manual upload path in the database.")
            relative = Path("spool", "manual", old.name)
            if not (stage / relative).is_file():
                raise ValueError("A manually staged PDF is missing from the backup.")
            job["manual_file"] = str(destination / relative)
            db.execute("UPDATE jobs SET data=? WHERE id=?", (json.dumps(job), key))


def restore(source, destination):
    destination.mkdir(mode=0o700, exist_ok=True)
    with lock_writer(destination):
        # Completed or active databases must never be overwritten. A spool can
        # exist after an interrupted restore and can be reused if byte-identical.
        if any(p.name not in {"sync.lock", "spool"} and not p.name.startswith(".restore-")
               for p in destination.iterdir()):
            raise ValueError("Restore requires an empty state directory.")
        with tempfile.TemporaryDirectory(dir=destination, prefix=".restore-") as temp:
            stage = Path(temp)
            counts = stage_copy(source, stage)
            relocate_manual_files(stage, destination)
            spool = destination / "spool"
            if spool.exists() or spool.is_symlink():
                if not (stage / "spool").is_dir() or not spool.is_dir():
                    raise ValueError("Existing spool differs from the backup.")
                check_copy(stage / "spool", spool, spool_files(stage / "spool"))
            elif (stage / "spool").is_dir():
                (stage / "spool").rename(spool)
            (stage / "sync.sqlite").rename(destination / "sync.sqlite")
        return counts


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("operation", choices=("backup", "restore"))
    parser.add_argument("source", type=Path)
    parser.add_argument("destination", type=Path)
    args = parser.parse_args()
    source, destination = args.source.resolve(), args.destination.resolve()
    if source == destination or source in destination.parents or destination in source.parents:
        parser.error("Source and destination must be separate directories.")
    os.umask(0o077)
    operation = backup if args.operation == "backup" else restore
    print(json.dumps(operation(source, destination)))


if __name__ == "__main__":
    try:
        main()
    except (OSError, sqlite3.Error, ValueError) as exc:
        sys.exit(str(exc))
