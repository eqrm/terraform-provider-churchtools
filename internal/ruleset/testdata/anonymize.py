#!/usr/bin/env python3
"""Anonymise raw live ruleset GET bodies for the public fixtures.

usage: anonymize.py IN_DIR OUT_DIR   (reads *.ruleset.json, writes anon-NNN.ruleset.json)

Rules, chosen so the normalizer sees the same structure and the same int/string
mix as on the live data, but nothing identifies a group, person or place:
  * every integer (and canonical integer string) with abs value > 4 is renumbered
    consistently across the whole corpus (the same id always maps to the same new
    id, a number and its string form to the same value). 0-4 are kept: they are
    flags and constants (isArchived 0, groupStatusId 1/2/4, sexId 1/2).
  * strings survive only if they are structural: `var` names, operators/keys,
    ChurchQuery words, member-status words, "groupmembership". Everything else
    (description, shorty, dterm titles, comments, free text, dates) becomes a
    placeholder; the same text maps to the same placeholder.
  * timestamps are replaced by a fixed one.
"""
import json, re, sys, pathlib

KEEP_STRINGS = {
    "person.id", "person.firstName", "person.lastName", "ChurchQuery", "person",
    "groupmembership", "active", "none", "requested", "to_delete", "waiting",
}
CANON_INT = re.compile(r"^(-?[1-9][0-9]*|0)$")
DATE = re.compile(r"^\d{4}-\d{2}-\d{2}")
ids, texts = {}, {}

def new_id(n):
    if n not in ids:
        ids[n] = 1000 + len(ids) * 7  # not contiguous, never collides with the kept 0-4
    return ids[n]

def anon(node, key=None, parent_key=None):
    if isinstance(node, dict):
        return {k: anon(v, k, key) for k, v in node.items()}
    if isinstance(node, list):
        return [anon(v, key, parent_key) for v in node]
    if isinstance(node, bool) or node is None:
        return node
    if isinstance(node, int):
        return node if abs(node) <= 4 else (new_id(node) if node > 0 else -new_id(-node))
    if isinstance(node, float):
        return node
    s = node
    if key in ("dynamicGroupUpdateStarted", "dynamicGroupUpdateFinished"):
        return "2000-01-01 00:00:00"
    if key == "var" or s in KEEP_STRINGS:
        return s
    if CANON_INT.match(s):
        n = int(s)
        return s if abs(n) <= 4 else str(new_id(n) if n > 0 else -new_id(-n))
    if DATE.match(s):
        return "2000-01-01"
    if key in ("method", "primaryEntityAlias", "personIdFieldName"):
        return s
    if s not in texts:
        texts[s] = "text-%d" % (len(texts) + 1)
    return texts[s]

src, dst = pathlib.Path(sys.argv[1]), pathlib.Path(sys.argv[2])
dst.mkdir(parents=True, exist_ok=True)
for i, f in enumerate(sorted(src.glob("*.ruleset.json"))):
    data = json.loads(f.read_text())
    (dst / ("anon-%03d.ruleset.json" % (i + 1))).write_text(json.dumps(anon(data), indent=1, ensure_ascii=False) + "\n")
print(len(list(dst.glob("*.ruleset.json"))), "files")
