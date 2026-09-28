"""Export exactly one Git commit, independent of index and working-tree flags."""

import io
from pathlib import Path
import subprocess


class CommittedSource:
    def __init__(self, repository, revision="HEAD"):
        self.repository = Path(repository)
        self.commit = (
            self.git("rev-parse", "--verify", f"{revision}^{{commit}}").decode().strip()
        )
        self.entries = []
        for record in self.git("ls-tree", "-rz", "--full-tree", self.commit).split(
            b"\0"
        ):
            if not record:
                continue
            metadata, name = record.split(b"\t", 1)
            mode, kind, oid = metadata.decode().split()
            path = Path(name.decode())
            if kind != "blob" or mode not in ("100644", "100755"):
                raise ValueError(
                    f"{path}: unsupported committed file mode {mode} (symlinks/submodules)"
                )
            if path.is_absolute() or ".." in path.parts or ".git" in path.parts:
                raise ValueError(f"unsafe committed path: {path}")
            self.entries.append((path, mode, oid))

    def git(self, *arguments, data=None):
        return subprocess.check_output(
            ["git", *arguments], cwd=self.repository, input=data
        )

    def export(self, destination):
        """Materialize blobs without checkout filters, attributes or disk reads."""
        destination = Path(destination)
        destination.mkdir(parents=True, exist_ok=False)
        objects = "".join(f"{oid}\n" for _, _, oid in self.entries).encode()
        stream = io.BytesIO(self.git("cat-file", "--batch", data=objects))
        for path, mode, expected in self.entries:
            oid, kind, size = stream.readline().decode().split()
            if oid != expected or kind != "blob":
                raise ValueError(f"unexpected Git object for {path}")
            content = stream.read(int(size))
            if len(content) != int(size) or stream.read(1) != b"\n":
                raise ValueError(f"truncated Git object for {path}")
            target = destination / path
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_bytes(content)
            target.chmod(0o755 if mode == "100755" else 0o644)
        return destination
