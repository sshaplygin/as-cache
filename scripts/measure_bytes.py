"""Compare object-count and byte-budget LRU on the same Meta GET prefix."""

import argparse
import csv
import json
from pathlib import Path
import re
import shlex
import subprocess
import tempfile

from oracle_trace import write_oracle
from trace_inputs import CATALOG, verify

ROOT = Path(__file__).resolve().parent.parent
PIN = "1d7415569978330ea95c9cff06a260630406f7e3"


def meta_requests(path, limit=2_000_000):
    count = 0
    with path.open() as source:
        for row in csv.DictReader(source):
            if not row["key"] or not row["op"].upper().startswith("GET"):
                continue
            repeats = int(row["op_count"])
            if repeats < 1:
                continue
            size = int(row["size"]) + int(row["key_size"])
            for _ in range(min(repeats, 65536)):
                yield row["key"], size
                count += 1
                if count >= limit:
                    return


def measure(binary, path, capacity, ignore, work):
    command = [
        str(binary),
        str(path),
        "oracleGeneralBin",
        "lru",
        str(capacity),
        "--ignore-obj-size",
        str(ignore).lower(),
        "--num-thread",
        "1",
    ]
    print("$ " + shlex.join(command), flush=True)
    output = subprocess.check_output(
        command, cwd=work, text=True, stderr=subprocess.STDOUT
    )
    print(output, end="" if output.endswith("\n") else "\n", flush=True)
    matches = re.findall(r"([0-9]+) req.*?miss ratio ([0-9.]+)", output)
    match = matches[-1] if matches else None
    if not match:
        raise ValueError(f"no simulator result: {output}")
    return {"requests": int(match[0]), "miss_ratio": float(match[1]), "output": output}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("traces", type=Path)
    parser.add_argument("output", type=Path)
    parser.add_argument("--libcachesim", type=Path, default=ROOT / ".tools/libCacheSim")
    args = parser.parse_args()
    lcs = args.libcachesim.resolve()
    commit = subprocess.check_output(
        ["git", "-C", str(lcs), "rev-parse", "HEAD"], text=True
    ).strip()
    if commit != PIN:
        parser.error("libCacheSim checkout differs from pinned commit")
    entry = next(
        item
        for item in json.loads(CATALOG.read_text())["files"]
        if item["file"] == "meta_kvcache_202206_1.csv"
    )
    source = args.traces / entry["file"]
    verify(source, entry)
    rows = []
    with tempfile.TemporaryDirectory(prefix="as-cache-bytes-") as temporary:
        work = Path(temporary)
        trace = work / "meta.bin"
        metadata = write_oracle(meta_requests(source), trace)
        for capacity in (2500, 5000, 10000, 20000, 40000):
            budget = int(capacity * metadata["mean_first_object_bytes"])
            objects = measure(lcs / "_build/bin/cachesim", trace, capacity, True, work)
            sized = measure(lcs / "_build/bin/cachesim", trace, budget, False, work)
            if (
                objects["requests"] != metadata["requests"]
                or sized["requests"] != metadata["requests"]
            ):
                raise ValueError(
                    f"simulator counts {objects['requests']}/{sized['requests']}, expected {metadata['requests']}: {objects['output']} {sized['output']}"
                )
            rows.append(
                {
                    "object_capacity": capacity,
                    "byte_capacity": budget,
                    "objects": objects,
                    "bytes": sized,
                    "request_miss_delta_pp": 100
                    * (sized["miss_ratio"] - objects["miss_ratio"]),
                }
            )
    result = {
        "commit": subprocess.check_output(
            ["git", "rev-parse", "HEAD"], cwd=ROOT, text=True
        ).strip(),
        "libcachesim_commit": PIN,
        "input": entry,
        **metadata,
        "size_semantics": "size + key_size; current request size; no metadata overhead",
        "budget_semantics": "object_capacity * mean first-seen size per distinct key, rounded down",
        "rows": rows,
    }
    args.output.write_text(json.dumps(result, indent=2) + "\n")


if __name__ == "__main__":
    main()
