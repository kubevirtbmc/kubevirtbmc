#!/usr/bin/env python3
"""Diff two Redfish-Service-Validator run directories.

Usage: service-diff.py [--fail-on-introduced] HEAD_DIR [BASE_DIR]

Each directory holds one validator run: stdout.txt (captured validator
stdout, carrying the PASS/WARN/FAIL summary table) and a dated subdirectory
with RedfishServiceValidatorDebug_*.log (carrying the verdict lines). FAIL
verdicts are logged as "- FAIL - <property> (<kind>): <error>", attributed to
the resource named by the most recent "Validating <uri>..." line, so a
failure signature is "uri: property-error".

Prints a markdown summary to stdout: result counts side by side, then the
signatures unique to each run ("introduced" / "fixed"). With
--fail-on-introduced, exits 1 when HEAD_DIR has failures BASE_DIR does not;
without BASE_DIR every HEAD_DIR failure counts as introduced (nothing to
prove it pre-existing).
"""

import argparse
import glob
import os
import re
import sys

SUMMARY_LABELS = {"PASS": "Pass", "WARN": "Warn", "FAIL": "Fail", "NOT TESTED": "Not Tested"}
RESULT_ORDER = ["Pass", "Warn", "Fail", "Not Tested"]
VALIDATING_RE = re.compile(r"- INFO - Validating (\S+)\.\.\.")
FAIL_RE = re.compile(r"- FAIL - (.*?)\s*$")


def parse(logdir):
    counts, errors = {}, set()

    stdout_path = os.path.join(logdir, "stdout.txt")
    if os.path.exists(stdout_path):
        with open(stdout_path, errors="replace") as f:
            lines = f.read().splitlines()
        for i, line in enumerate(lines):
            cells = [c.strip() for c in line.split("|") if c.strip()]
            if cells and all(c in SUMMARY_LABELS for c in cells):
                # The summary is a horizontal table: one row of labels, then
                # a separator, then a row of counts in the same column order.
                for row in lines[i + 1:]:
                    values = [c.strip() for c in row.split("|") if c.strip()]
                    if values and all(v.isdigit() for v in values):
                        for label, value in zip(cells, values):
                            counts[SUMMARY_LABELS[label]] = int(value)
                        break
                break

    for path in glob.glob(os.path.join(logdir, "**", "RedfishServiceValidatorDebug_*.log"),
                          recursive=True):
        resource = "(run)"
        with open(path, errors="replace") as f:
            for line in f:
                m = VALIDATING_RE.search(line)
                if m:
                    resource = m.group(1)
                    continue
                m = FAIL_RE.search(line)
                if m:
                    errors.add(f"{resource}: {m.group(1)}")
    return counts, errors


def fmt_count(counts, key):
    return str(counts[key]) if key in counts else "n/a"


def fmt_delta(head, base, key):
    if key not in head or key not in base:
        return ""
    delta = head[key] - base[key]
    return f"{delta:+d}" if delta else "0"


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--fail-on-introduced", action="store_true",
                        help="exit 1 if HEAD_DIR has failures BASE_DIR does not "
                             "(without BASE_DIR: any HEAD_DIR failure)")
    parser.add_argument("head_dir")
    parser.add_argument("base_dir", nargs="?")
    args = parser.parse_args()

    head_counts, head_errors = parse(args.head_dir)
    base_counts, base_errors = parse(args.base_dir) if args.base_dir else ({}, set())
    introduced = sorted(head_errors - base_errors)

    # Fail>0 with no parsed FAIL lines means the validator's log format
    # drifted from what parse() expects (verified against 3.1.7); without
    # this warning --fail-on-introduced would pass vacuously.
    if head_counts.get("Fail", 0) > 0 and not head_errors:
        print("**Warning:** summary reports FAIL > 0 but no FAIL lines were parsed "
              "from RedfishServiceValidatorDebug_*.log — validator log format may "
              "have changed, the failure lists below may be incomplete.\n")

    print("## Redfish Service Validator\n")
    if args.base_dir:
        print("| Result | base | PR head | delta |")
        print("|--------|-----:|--------:|------:|")
        for key in RESULT_ORDER:
            print(f"| {key} | {fmt_count(base_counts, key)} | "
                  f"{fmt_count(head_counts, key)} | {fmt_delta(head_counts, base_counts, key)} |")
        print()

        fixed = sorted(base_errors - head_errors)
        print(f"### Introduced by this PR ({len(introduced)})\n")
        print("\n".join(f"- `{e}`" for e in introduced) or "- none", end="\n\n")
        print(f"### Fixed by this PR ({len(fixed)})\n")
        print("\n".join(f"- `{e}`" for e in fixed) or "- none", end="\n\n")
    else:
        print("| Result | Count |")
        print("|--------|------:|")
        for key in RESULT_ORDER:
            print(f"| {key} | {fmt_count(head_counts, key)} |")
        print()
        print(f"### Failures ({len(introduced)})\n")
        print("\n".join(f"- `{e}`" for e in introduced) or "- none", end="\n\n")

    if os.environ.get("GITHUB_ACTIONS"):
        print("Full detail: see the uploaded `redfish-service-validator-logs` artifact "
              "(HTML report per run).")
    else:
        print(f"Full detail: HTML report under `{args.head_dir}`.")

    if args.fail_on_introduced and introduced:
        print(f"\n**Regression gate: {len(introduced)} introduced failure(s).**")
        sys.exit(1)


if __name__ == "__main__":
    main()
