"""Render current trace tables directly from retained measurements."""

from collections import Counter
import json
from pathlib import Path
from statistics import median
import sys


def spread(values):
    return f"{median(values):.2f}% [{min(values):.2f}–{max(values):.2f}]"


def fixed_rates(trace):
    return {
        name: median(value["runs"])
        for name, value in trace["fixed_hit_rate_percent"].items()
    }


def baseline(trace, value):
    fixed = trace["fixed_hit_rate_percent"]
    return "<br>".join(
        f"{name} {spread(fixed[name]['runs'])}"
        for name, rate in sorted(fixed_rates(trace).items())
        if rate == value
    )


def report_text(directory):
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
        "All nine arms; request-counted epochs. Actual constructor settings are retained for every adaptive and ObserveOnly cell.",
        "Effective sample rates come from the measured caches' Advice and are shown in the context table.",
        "",
        "| Trace | Best fixed median [min–max] (ties retained) | Worst fixed median [min–max] (ties retained) | 10 epochs | 20 epochs | 50 epochs |",
        "| --- | --- | --- | --- | --- | --- |",
    ]
    below = []
    deficits = Counter()
    below_every_epoch = 0
    above_every_epoch = []
    for trace in traces:
        rates = fixed_rates(trace)
        best, worst = max(rates.values()), min(rates.values())
        cells = []
        gaps = []
        for adaptive in trace["adaptive"]:
            values = adaptive["hit_rate_percent"]["runs"]
            gap = median(values) - best
            gaps.append(gap)
            deficits[adaptive["epochs_per_trace"]] += gap < 0
            if median(values) < worst:
                below.append(
                    (
                        trace["trace"],
                        adaptive["epochs_per_trace"],
                        median(values) - worst,
                        baseline(trace, worst),
                    )
                )
            cells.append(f"{spread(values)} ({gap:+.2f} pp)")
        lines.append(
            f"| {trace['trace']} | {baseline(trace, best)} | {baseline(trace, worst)} | "
            + " | ".join(cells)
            + " |"
        )
        below_every_epoch += all(gap < 0 for gap in gaps)
        if all(gap > 0 for gap in gaps):
            above_every_epoch.append(trace["trace"])
    lines += [
        "",
        f"Adaptive medians trail the best fixed median on {below_every_epoch}/{len(traces)} traces at every tested epoch setting.",
        "Counts below the best fixed median by setting: "
        + "; ".join(
            f"{epochs} epochs: {deficits[epochs]}/{len(traces)}"
            for epochs in (10, 20, 50)
        )
        + ".",
        "Traces above the best fixed median at every setting: "
        + (", ".join(above_every_epoch) or "none")
        + ".",
        "The table compares medians with the best fixed median in this dataset. It makes no claim of a universal maximum deficit.",
        "W-TinyLFU is asynchronous: a short batch may miss another performance mode, especially on LIRS loop.",
        "A winning policy name is not evidence of a material or statistically established advantage.",
        "",
        "Adaptive medians below the worst fixed median in this dataset:",
        "",
    ]
    if below:
        lines += [
            f"- {name}, {epochs} epochs: {gap:+.4f} percentage points versus {worst}."
            for name, epochs, gap, worst in below
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
        "Use the margins and ties above when interpreting policy names; small differences do not establish a useful ranking.",
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
    modal_agreement = individual_agreement = total_runs = 0
    mismatches = []
    for trace in traces:
        counts = Counter(
            trace["policy_names"][run["best_policy_name"]]
            for run in trace["observe_only"]
        )
        rates = fixed_rates(trace)
        best = max(rates.values())
        winners = {name for name, rate in rates.items() if rate == best}
        modes = sorted(
            name for name, count in counts.items() if count == max(counts.values())
        )
        modal_agreement += all(name in winners for name in modes)
        individual_agreement += sum(
            count for name, count in counts.items() if name in winners
        )
        total_runs += sum(counts.values())
        lines.append(
            f"| {trace['trace']} | {spread([r['hit_rate_percent'] for r in trace['observe_only']])} | "
            + ", ".join(f"{name}: {count}" for name, count in sorted(counts.items()))
            + " |"
        )
        for name in modes:
            if name not in winners:
                mismatches.append((trace, name, rates[name] - best))
    lines += [
        "",
        f"Retrospective modal agreement: {modal_agreement}/{len(traces)} traces. Each trace counts once, and all tied modal choices must belong to the tied best-fixed set to count as agreement.",
        f"Individual final recommendations in the best-fixed set: {individual_agreement}/{total_runs} ({100 * individual_agreement / total_runs:.2f}%). Every run counts once; any tied best-fixed arm counts as agreement.",
        "These compare final sampled Advice choices with standalone full-cache medians in this dataset, not forecast accuracy or a causal cost of following Advice. Serving remained LRU.",
    ]
    if mismatches:
        lines += [
            "",
            "Modal mismatches (each tied nonwinning mode has its own row):",
            "",
            "| Trace | Modal recommendation: standalone median [min–max] | Best fixed median [min–max] | Standalone median difference |",
            "| --- | --- | --- | --- |",
        ]
        for trace, name, gap in mismatches:
            lines.append(
                f"| {trace['trace']} | {name} {spread(trace['fixed_hit_rate_percent'][name]['runs'])} | "
                f"{baseline(trace, max(fixed_rates(trace).values()))} | {gap:+.4f} pp |"
            )
        trace, name, gap = mismatches[0]
        lines += [
            "",
            f"For example, on {trace['trace']} the modal {name} recommendation has a standalone median {gap:+.4f} points relative to the best fixed median. This subtraction compares separate full-cache replays; the sampled ObserveOnly cache did not switch to {name} or measure that difference as a serving loss.",
        ]
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
        "Every cell retains its actual migration, sampling and stability settings in tuning.json; nine arms are measured.",
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
        "The pinned input catalog has 13 files. lirs_multi2.trace.gz is inventoried but not replayed in the 12-trace matrix or reference gate.",
        "Verify with `python3 scripts/record_evidence.py --verify bench/results/current`.",
        "Reference calibration compares independent expansions of the same interpretation and LRU counting; it cannot detect a shared interpretation error.",
        "The reference accepts finite ratios in [0, 1] with exactly four decimals. Tolerance is half that rounding quantum plus numerical slack: 0.0051 percentage points. All loaded traces must have reference coverage.",
        "The byte experiment uses the Meta prefix only. Other traces remain entry-capacity, not equal-memory comparisons.",
        "Per-operation timings and wall-clock experiments remain raw diagnostics in the logs, not stable product claims.",
        "",
        "Reproduce: `AS_CACHE_TRACES=$PWD/traces python3 scripts/record_evidence.py --out <temporary-output-directory>`.",
        "A complete run requires the pinned libCacheSim build and three sequential evidence runs. Replace current results after validation; do not create a historical archive.",
        "",
    ]
    return "\n".join(lines)


def render(directory):
    (directory / "README.md").write_text(report_text(directory))


if __name__ == "__main__":
    render(Path(sys.argv[1]))
