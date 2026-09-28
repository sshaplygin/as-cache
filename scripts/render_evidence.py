"""Render current trace tables directly from retained measurements."""

import json
from pathlib import Path
from statistics import median
import sys


def spread(values):
    return f"{median(values):.2f}% [{min(values):.2f}–{max(values):.2f}]"


def render(directory):
    data = json.loads((directory / "traces.json").read_text())
    traces = data["traces"]
    lines = [
        "# Current measurement results",
        "",
        f"Measured source: `{data['commit']}`; clean committed trees; three consecutive full evidence runs.",
        "Nondeterministic subjects have 15 observations (three batches of five). Deterministic fixed arms run once per batch.",
        "Median [min–max] describes these observations, not a confidence interval or a bound on future runs.",
        "All trace-matrix outcomes are retained; relative hit rates do not decide whether this matrix passes.",
        "",
        "## Trace matrix",
        "",
        "All nine arms; warm migration; request-counted epochs; configured sampling 5%, floor 64.",
        "This experimental floor differs from the library default 256. Effective rates are in the context table.",
        "",
        "| Trace | Best fixed median (ties retained) | Worst fixed median | 10 epochs | 20 epochs | 50 epochs |",
        "| --- | --- | --- | --- | --- | --- |",
    ]
    gaps = []
    below = []
    for trace in traces:
        fixed = trace["fixed_hit_rate_percent"]
        rates = {name: median(value["runs"]) for name, value in fixed.items()}
        best, worst = max(rates.values()), min(rates.values())
        winners = "/".join(sorted(name for name, rate in rates.items() if rate == best))
        cells = []
        for adaptive in trace["adaptive"]:
            values = adaptive["hit_rate_percent"]["runs"]
            gap = median(values) - best
            gaps.append(gap)
            if median(values) < worst:
                below.append(
                    (
                        trace["trace"],
                        adaptive["epochs_per_trace"],
                        median(values) - worst,
                    )
                )
            cells.append(f"{spread(values)} ({gap:+.2f} pp)")
        lines.append(
            f"| {trace['trace']} | {winners} {best:.2f}% | {worst:.2f}% | "
            + " | ".join(cells)
            + " |"
        )
    lines += [
        "",
        "The table compares medians with the best fixed median in this dataset. It makes no claim of a universal maximum deficit.",
        "W-TinyLFU is asynchronous: a short batch may miss another performance mode, especially on LIRS loop.",
        "A winning policy name is not evidence of a material or statistically established advantage.",
        "",
        "Adaptive medians below the worst fixed median in this dataset:",
        "",
    ]
    if below:
        lines += [
            f"- {name}, {epochs} epochs: {gap:+.4f} percentage points."
            for name, epochs, gap in below
        ]
    else:
        lines += [
            "None observed. This comparison is reported, not asserted; future runs can fall below it."
        ]
    lines += [
        "",
        "## Workload context",
        "",
        "Compulsory-miss ceiling assumes an empty cache: 100 × (requests − distinct keys) / requests.",
        "The best/runner-up gap includes ties; tiny gaps cannot support a strong policy ranking.",
        "",
        "| Trace | Requests | Distinct keys | Capacity | Capacity/keyspace | Effective sample | Hit ceiling | Best–runner-up | Best–worst |",
        "| --- | --- | --- | --- | --- | --- | --- | --- | --- |",
    ]
    for trace in traces:
        rates = sorted(
            (
                median(value["runs"])
                for value in trace["fixed_hit_rate_percent"].values()
            ),
            reverse=True,
        )
        lines.append(
            f"| {trace['trace']} | {trace['requests']} | {trace['distinct_keys']} | {trace['capacity']} | "
            f"{100 * trace['capacity'] / trace['distinct_keys']:.3f}% | {100 * trace['effective_sample_rate']:.2f}% | "
            f"{100 * (1 - trace['distinct_keys'] / trace['requests']):.2f}% | {rates[0] - rates[1]:.4f} pp | {rates[0] - rates[-1]:.4f} pp |"
        )
    lines += [
        "",
        "On MSR prn and web the leading LFU/SIEVE rates tie exactly; on src1_2 and usr the best–runner-up margins are below 0.04 points.",
        "The cache is roughly 1% of the distinct keyspace on most MSR prefixes; the ceilings above limit the available hit-rate signal.",
        "",
        "## Every fixed arm",
        "",
        "| Trace | Policy | Hit rate |",
        "| --- | --- | --- |",
    ]
    for trace in traces:
        for name, value in sorted(trace["fixed_hit_rate_percent"].items()):
            lines.append(f"| {trace['trace']} | {name} | {spread(value['runs'])} |")
    lines += [
        "",
        "## ObserveOnly",
        "",
        "LRU remains active, all nine arms are measured, 20 request epochs per replay, the same sample/floor settings.",
        "Every run asserts serving hits equal standalone LRU. Advice rankings measure sampled shadows; they are not full-cache forecasts.",
        "The table counts the final Advice.Best choices across 15 runs; all per-arm hit/miss reports are in traces.json.",
        "",
        "| Trace | Serving hit rate | Recommended policies (count) |",
        "| --- | --- | --- |",
    ]
    names = {
        1: "LRU",
        2: "LFU",
        3: "2Q",
        4: "ARC",
        5: "Random",
        6: "TTL",
        7: "W-TinyLFU",
        8: "S3-FIFO",
        9: "SIEVE",
    }
    for trace in traces:
        counts = {}
        for run in trace["observe_only"]:
            name = names.get(run["advice"]["Best"], str(run["advice"]["Best"]))
            counts[name] = counts.get(name, 0) + 1
        lines.append(
            f"| {trace['trace']} | {spread([r['hit_rate_percent'] for r in trace['observe_only']])} | "
            + ", ".join(f"{name}: {count}" for name, count in sorted(counts.items()))
            + " |"
        )
    lines += [
        "",
        "This is an offline sweep, not a service trial or proof that following Advice will improve production traffic.",
        "",
        "## LFU/SIEVE diagnostic",
        "",
        "Each request is checked against an independent visited-bit/hand model. A FIFO control and LFU run on the identical stream.",
        "",
        "| Trace | LFU hits | SIEVE hits | FIFO hits | LFU/SIEVE decisions differ | SIEVE/model disagreements |",
        "| --- | --- | --- | --- | --- | --- |",
    ]
    for trace in traces:
        d = trace["lfu_sieve_diagnostic"]
        lines.append(
            f"| {trace['trace']} | {d['lfu_hits']} | {d['sieve_hits']} | {d['fifo_hits']} | {d['different_lfu_sieve_decisions']} | {d['sieve_reference_disagreements']} |"
        )
    lines += [
        "",
        "Equal hit/miss streams do not imply equal eviction state. The nonzero FIFO differences rule out a general reduction to FIFO.",
        "A capacity-2 counterexample separates the mechanisms: a,a,b,b,c,d,a,b gives LFU 3 hits and SIEVE 2; a,b,a,c,a gives SIEVE 2 and FIFO 1.",
        "TestSieveLFUDistinction pins both examples. The real-trace model check tests the adapter without assuming these policies must have different totals.",
        "",
        "## P3 tuning",
        "",
        "This configuration example is restricted to P3; it does not identify a universal production setting.",
        "Gates mean MinHitRateImprovement=0.02 and SwitchCooldownEpochs=3. All cells keep nine arms, 5% requested sampling and floor 64.",
        "",
        "| Epochs | Migration | Gates | Hit rate |",
        "| --- | --- | --- | --- |",
    ]
    for row in json.loads((directory / "tuning.json").read_text())["records"]:
        lines.append(
            f"| {row['epochs']} | {row['strategy']} | {row['gates']} | {spread(row['hit_rate_percent']['runs'])} |"
        )
    byte_data = json.loads((directory / "bytes.json").read_text())
    lines += [
        "",
        "## Meta object-count versus byte capacity",
        "",
        f"Pinned libCacheSim `{byte_data['libcachesim_commit']}`, LRU, the same {byte_data['requests']} GET requests in both modes.",
        "The export retains request-time size + key_size. The pinned simulator charges insertion-time size until eviction; size changes on hits do not resize resident objects. Metadata overhead is excluded.",
        f"Byte budget = object capacity × mean first-seen size per distinct key ({byte_data['mean_first_object_bytes']:.3f} bytes), rounded down.",
        "This is a stated budget convention, not proof of equal resident memory. Deltas are request miss ratios, not byte-weighted miss ratios.",
        "",
        "| Objects | Bytes | Ignore sizes: miss | Account sizes: miss | Difference |",
        "| --- | --- | --- | --- | --- |",
    ]
    for row in byte_data["rows"]:
        lines.append(
            f"| {row['object_capacity']} | {row['byte_capacity']} | {100 * row['objects']['miss_ratio']:.2f}% | {100 * row['bytes']['miss_ratio']:.2f}% | {row['request_miss_delta_pp']:+.2f} pp |"
        )
    lines += [
        "",
        "## Provenance and limits",
        "",
        "manifest.json is generated by scripts/record_evidence.py and hashes every retained input/output.",
        "Verify with `python3 scripts/record_evidence.py --verify bench/results/current`.",
        "Reference calibration compares independent expansions of the same interpretation and LRU counting; it cannot detect a shared interpretation error.",
        "Its tolerance is 0.0051 percentage points, just above the simulator's four-decimal rounding limit. All loaded traces must have reference coverage.",
        "The byte experiment uses the Meta prefix only. Other traces remain entry-capacity, not equal-memory comparisons.",
        "Per-operation timings and wall-clock experiments remain raw diagnostics in the logs, not stable product claims.",
        "",
        "Reproduce: `AS_CACHE_TRACES=$PWD/traces python3 scripts/record_evidence.py --out <temporary-output-directory>`.",
        "A complete run requires the pinned libCacheSim build and three sequential evidence runs. Replace current results after validation; do not create a historical archive.",
        "",
    ]
    (directory / "README.md").write_text("\n".join(lines))


if __name__ == "__main__":
    render(Path(sys.argv[1]))
