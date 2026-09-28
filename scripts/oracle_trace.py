"""Export keys and sizes as little-endian libCacheSim oracleGeneralBin records."""

import argparse
from array import array
from pathlib import Path
import struct

RECORD = struct.Struct("<IQIq")


def write_oracle(requests, destination):
    ids = {}
    keys, sizes = array("Q"), array("I")
    first_sizes = {}
    for key, size in requests:
        if not 0 < size < 2**32:
            raise ValueError(f"invalid object size: {size}")
        identity = ids.setdefault(key, len(ids) + 1)
        first_sizes.setdefault(identity, size)
        keys.append(identity)
        sizes.append(size)
    next_use = array("q", [-1]) * len(keys)
    last = {}
    for index in range(len(keys) - 1, -1, -1):
        next_use[index] = last.get(keys[index], -1)
        last[keys[index]] = index + 1
    with destination.open("wb") as output:
        for index, (key, size, future) in enumerate(zip(keys, sizes, next_use)):
            output.write(RECORD.pack(index, key, size, future))
    return {
        "requests": len(keys),
        "distinct_keys": len(ids),
        "mean_first_object_bytes": sum(first_sizes.values()) / len(ids),
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("source", type=Path)
    parser.add_argument("destination", type=Path)
    args = parser.parse_args()
    with args.source.open() as source:
        write_oracle(((line.rstrip("\n"), 1) for line in source), args.destination)


if __name__ == "__main__":
    main()
