"""Validate and pool repeated measurements without discarding observations."""

from copy import deepcopy
import json
import math

ARMS = {"LRU", "LFU", "2Q", "ARC", "TTL", "Random", "W-TinyLFU", "S3-FIFO", "SIEVE"}
EPOCHS = [10, 20, 50]


def check_rates(values, count):
    if len(values) != count or any(
        not isinstance(value, (int, float))
        or not math.isfinite(value)
        or not 0 <= value <= 100
        for value in values
    ):
        raise ValueError("unexpected hit-rate observations or sample count")


def metadata(value, excluded):
    return {key: item for key, item in value.items() if key not in excluded}


def read_batches(directory, commit):
    batches = [
        json.loads((directory / f"traces-{i}.json").read_text()) for i in range(1, 4)
    ]
    first = batches[0]
    inventory = [trace["trace"] for trace in first["traces"]]
    if not inventory or len(inventory) != len(set(inventory)):
        raise ValueError("empty or duplicate trace inventory")
    for batch in batches:
        if batch["commit"] != commit or batch["tree_modified"]:
            raise ValueError("measurement came from another commit or a dirty tree")
        if batch["runs"] != 5:
            raise ValueError("expected five observations per nondeterministic subject")
        if metadata(batch, {"measured_at", "traces"}) != metadata(
            first, {"measured_at", "traces"}
        ):
            raise ValueError("measurement settings changed between runs")
        if [trace["trace"] for trace in batch["traces"]] != inventory:
            raise ValueError("trace inventory changed between runs")
        for trace, original in zip(batch["traces"], first["traces"], strict=True):
            excluded = {"fixed_hit_rate_percent", "adaptive", "observe_only"}
            if metadata(trace, excluded) != metadata(original, excluded):
                raise ValueError(
                    "trace metadata or deterministic diagnostics changed between runs"
                )
            fixed = trace["fixed_hit_rate_percent"]
            if set(fixed) != ARMS:
                raise ValueError("fixed-policy inventory changed")
            for name, spread in fixed.items():
                check_rates(spread["runs"], 5 if name in {"Random", "W-TinyLFU"} else 1)
            if [row["epochs_per_trace"] for row in trace["adaptive"]] != EPOCHS:
                raise ValueError("unexpected adaptive epoch inventory")
            for row, previous in zip(
                trace["adaptive"], original["adaptive"], strict=True
            ):
                if metadata(row, {"hit_rate_percent"}) != metadata(
                    previous, {"hit_rate_percent"}
                ):
                    raise ValueError("epoch settings changed between runs")
                check_rates(row["hit_rate_percent"]["runs"], 5)
            check_rates([run["hit_rate_percent"] for run in trace["observe_only"]], 5)
    return batches


def pooled_results(directory, commit):
    batches = read_batches(directory, commit)
    combined = deepcopy(batches[0])
    combined["batches"] = 3
    combined["runs_per_batch"] = combined.pop("runs")
    for index, trace in enumerate(combined["traces"]):
        for batch in batches[1:]:
            other = batch["traces"][index]
            for name in trace["fixed_hit_rate_percent"]:
                trace["fixed_hit_rate_percent"][name]["runs"].extend(
                    other["fixed_hit_rate_percent"][name]["runs"]
                )
            for adaptive, repeated in zip(
                trace["adaptive"], other["adaptive"], strict=True
            ):
                adaptive["hit_rate_percent"]["runs"].extend(
                    repeated["hit_rate_percent"]["runs"]
                )
            trace["observe_only"].extend(other["observe_only"])

    tuning = None
    expected = [
        (epochs, strategy, gates)
        for epochs in EPOCHS
        for strategy in ("cold", "warm")
        for gates in (False, True)
    ]
    for number in range(1, 4):
        rows = json.loads((directory / f"traces-{number}-tuning.json").read_text())
        if [(row["epochs"], row["strategy"], row["gates"]) for row in rows] != expected:
            raise ValueError("unexpected tuning inventory")
        for row in rows:
            check_rates(row["hit_rate_percent"]["runs"], 5)
        if tuning is None:
            tuning = rows
            continue
        for row, repeated in zip(tuning, rows, strict=True):
            if metadata(row, {"hit_rate_percent"}) != metadata(
                repeated, {"hit_rate_percent"}
            ):
                raise ValueError("tuning settings changed between runs")
            row["hit_rate_percent"]["runs"].extend(repeated["hit_rate_percent"]["runs"])
    return combined, {"commit": commit, "records": tuning}
