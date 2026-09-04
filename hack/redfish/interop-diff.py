#!/usr/bin/env python3
"""Diff two Redfish-Interop-Validator run directories.

Usage: interop-diff.py [--fail-on-introduced] HEAD_DIR [BASE_DIR]

Each directory holds one validator run: stdout.txt (captured validator stdout,
carrying the Results Summary table) and ConformanceLog_*.txt (carrying the
verdict lines). FAIL verdicts are logged at ERROR level as
"ERROR - <msg> ... <verdict> at <uri>", each appearing both inline and in the
closing listing, hence the set dedupe.

Prints a markdown summary to stdout: result counts side by side, then the
ERROR lines unique to each run ("introduced" / "fixed"). With
--fail-on-introduced, exits 1 when HEAD_DIR has ERROR lines BASE_DIR does not;
without BASE_DIR every HEAD_DIR error counts as introduced (nothing to prove
it pre-existing).
"""

import argparse
import glob
import os
import re
import sys

SUMMARY_RE = re.compile(r"^\|\s*(Pass|Fail|Warning|Not Tested)\s*\|\s*(\d+)\s*\|", re.M)
ERROR_RE = re.compile(r"^ERROR - (.*?)(?:\s*\.\.\.\s*)?$")
RESULT_ORDER = ["Pass", "Fail", "Warning", "Not Tested"]


def parse(logdir):
    counts, errors = {}, set()

    stdout_path = os.path.join(logdir, "stdout.txt")
    if os.path.exists(stdout_path):
        with open(stdout_path, errors="replace") as f:
            for name, count in SUMMARY_RE.findall(f.read()):
                counts[name] = int(count)

    for path in glob.glob(os.path.join(logdir, "ConformanceLog_*.txt")):
        with open(path, errors="replace") as f:
            for line in f:
                m = ERROR_RE.match(line)
                if m:
                    errors.add(m.group(1))
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

    # Fail>0 with no parsed ERROR lines means the validator's log format
    # drifted from what parse() expects (verified against 2.3.6); without
    # this warning --fail-on-introduced would pass vacuously.
    if head_counts.get("Fail", 0) > 0 and not head_errors:
        print("**Warning:** summary reports Fail > 0 but no ERROR lines were parsed "
              "from ConformanceLog_*.txt — validator log format may have changed, "
              "the failure lists below may be incomplete.\n")

    print("## Redfish Interop Validator\n")
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
        print("Full detail: see the uploaded `redfish-interop-logs` artifact "
              "(InteropHtmlLog per run).")
    else:
        print(f"Full detail: InteropHtmlLog under `{args.head_dir}`.")

    if args.fail_on_introduced and introduced:
        print(f"\n**Regression gate: {len(introduced)} introduced failure(s).**")
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
