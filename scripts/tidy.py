"""Tidy modules whose sibling versions can resolve without the workspace."""

import json
import os
import subprocess

from release_check import MODULE, ROOT, module_directories, run


def main():
    env = dict(os.environ, GOWORK="off")
    resolved = {}
    for directory in module_directories():
        path = ROOT / directory
        metadata = json.loads(run("go", "mod", "edit", "-json", cwd=path, env=env))
        replaced = {entry["Old"]["Path"] for entry in metadata.get("Replace") or []}
        missing = []
        for dependency in metadata.get("Require") or []:
            name = dependency["Path"]
            if (
                name != MODULE and not name.startswith(MODULE + "/")
            ) or name in replaced:
                continue
            version = name + "@" + dependency["Version"]
            if version not in resolved:
                result = subprocess.run(
                    ["go", "list", "-m", version],
                    cwd=path,
                    env=env,
                    capture_output=True,
                    text=True,
                )
                resolved[version] = result.returncode == 0
            if not resolved[version]:
                missing.append(version)
        if missing:
            print(
                f"SKIP tidy {directory}: unresolved sibling versions: {', '.join(missing)}"
            )
            continue
        print(f"tidy {directory}", flush=True)
        subprocess.run(["go", "mod", "tidy"], cwd=path, env=env, check=True)


if __name__ == "__main__":
    main()
