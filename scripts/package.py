"""Package the listed files with fixed timestamps, permissions and ownership."""

import gzip
from pathlib import Path
import sys
import tarfile
import tempfile
import zipfile


def write_tar(output, root, names):
    with output.open("wb") as raw, gzip.GzipFile(filename="", fileobj=raw, mode="wb", mtime=0) as gz:
        with tarfile.open(fileobj=gz, mode="w", format=tarfile.USTAR_FORMAT) as archive:
            for parent in sorted({parent for name in names for parent in name.parents}):
                directory = tarfile.TarInfo((Path(root.name) / parent).as_posix())
                directory.type = tarfile.DIRTYPE
                directory.mode = 0o755
                archive.addfile(directory)
            for name in names:
                path = root / name
                info = tarfile.TarInfo((Path(root.name) / name).as_posix())
                info.size = path.stat().st_size
                info.mode = 0o755 if name.as_posix() in {"bin/kindle-sync", "deploy/install.sh"} else 0o644
                with path.open("rb") as data:
                    archive.addfile(info, data)


def write_zip(output, root, names):
    with zipfile.ZipFile(output, "w") as archive:
        for name in names:
            info = zipfile.ZipInfo(name.as_posix())
            info.create_system = 3
            info.external_attr = 0o100644 << 16
            archive.writestr(info, (root / name).read_bytes(), compress_type=zipfile.ZIP_DEFLATED)


if __name__ == "__main__":
    kind, source, destination, *members = sys.argv[1:]
    write = {"tar": write_tar, "zip": write_zip}[kind]
    output = Path(destination)
    with tempfile.TemporaryDirectory(dir=output.parent) as directory:
        temp = Path(directory) / "archive"
        write(temp, Path(source), [Path(name) for name in members])
        temp.replace(output)
