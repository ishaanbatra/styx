#!/usr/bin/env python3
"""Bucket promptfoo eval misses by want-category for fast iteration.

Usage: python3 buckets.py /tmp/out.json [variant-label]

Reads promptfoo's JSON output, prints per-variant accuracy and every MISS
grouped by the want_* label so we can see which buckets to fix next. Defensive
about promptfoo's result schema (it shifts between versions).
"""
import json
import sys
from collections import defaultdict


def load_results(path):
    with open(path) as f:
        doc = json.load(f)
    # promptfoo nests results under results.results (newer) or top-level results.
    r = doc.get("results", doc)
    if isinstance(r, dict):
        r = r.get("results", r)
    if not isinstance(r, list):
        raise SystemExit("could not find results array in %s" % path)
    return r


def want_str(v):
    s = v.get("want_action", "") or ""
    if v.get("want_thread"):
        s += "/" + v["want_thread"]
    if v.get("want_pipeline"):
        s += "/" + v["want_pipeline"]
    return s


def variant_of(row):
    p = row.get("prompt") or {}
    return p.get("label") or p.get("id") or "?"


def reason_of(row):
    gr = row.get("gradingResult") or {}
    if gr.get("reason"):
        return gr["reason"]
    comps = gr.get("componentResults") or []
    for c in comps:
        if c.get("reason"):
            return c["reason"]
    return row.get("failureReason") or row.get("error") or "?"


def success_of(row):
    if "success" in row:
        return bool(row["success"])
    gr = row.get("gradingResult") or {}
    return bool(gr.get("pass"))


def main():
    path = sys.argv[1] if len(sys.argv) > 1 else "/tmp/out.json"
    only = sys.argv[2] if len(sys.argv) > 2 else None
    rows = load_results(path)

    by_variant = defaultdict(list)
    for row in rows:
        by_variant[variant_of(row)].append(row)

    for variant in sorted(by_variant):
        if only and variant != only:
            continue
        vrows = by_variant[variant]
        total = len(vrows)
        misses = [r for r in vrows if not success_of(r)]
        correct = total - len(misses)
        pct = 100.0 * correct / total if total else 0.0
        print("=" * 70)
        print("VARIANT %s: %d/%d = %.1f%%  (%d misses)" % (variant, correct, total, pct, len(misses)))
        print("=" * 70)
        buckets = defaultdict(list)
        for r in misses:
            v = r.get("vars") or {}
            buckets[want_str(v)].append((v.get("utterance", "?"), reason_of(r)))
        for b in sorted(buckets, key=lambda k: -len(buckets[k])):
            print("\n[want %s]  (%d miss)" % (b, len(buckets[b])))
            for utt, reason in buckets[b]:
                print("  - %s" % utt)
                print("      %s" % reason)
        print()


if __name__ == "__main__":
    main()
