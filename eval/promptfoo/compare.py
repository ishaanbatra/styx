#!/usr/bin/env python3
"""Compare promptfoo variants per-fixture against the baseline.

Usage: python3 compare.py /tmp/pf_v12.json
Prints per-variant accuracy, fixed-vs-baseline, and regressions-vs-baseline.
"""
import json
import sys
from collections import defaultdict

d = json.load(open(sys.argv[1]))
rows = d["results"]["results"]

# label per result: prompt label -> {utterance -> pass}
by_label = defaultdict(dict)
got_by_label = defaultdict(dict)
for r in rows:
    label = r.get("promptId") or ""
    # promptfoo stores the prompt label in r["prompt"]["label"] or r["testCase"]
    label = (r.get("prompt") or {}).get("label") or label
    utt = r["vars"]["utterance"]
    by_label[label][utt] = bool(r.get("success"))
    got_by_label[label][utt] = (r.get("response") or {}).get("output", "")[:80]

labels = list(by_label.keys())
base = "baseline"
print("=== accuracy ===")
for lb in labels:
    p = sum(by_label[lb].values())
    n = len(by_label[lb])
    print(f"  {lb:10s} {p}/{n} = {100*p/n:.1f}%")

if base in by_label:
    for lb in labels:
        if lb == base:
            continue
        fixed = [u for u in by_label[lb] if by_label[lb][u] and not by_label[base].get(u, True)]
        regr = [u for u in by_label[lb] if not by_label[lb][u] and by_label[base].get(u, False)]
        print(f"\n=== {lb} vs baseline ===")
        print(f"  FIXED ({len(fixed)}):")
        for u in fixed:
            print(f"    + {u}")
        print(f"  REGRESSED ({len(regr)}):")
        for u in regr:
            print(f"    - {u}  -> got {got_by_label[lb][u]}")
        still = [u for u in by_label[lb] if not by_label[lb][u]]
        print(f"  STILL FAILING ({len(still)}):")
        for u in still:
            print(f"    x {u}  -> got {got_by_label[lb][u]}")
