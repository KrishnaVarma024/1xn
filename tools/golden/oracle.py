#!/usr/bin/env python3
"""Reference oracle for the 1x.n pricing and GST engine.

This is a second, independent implementation of the rules in
docs/learn/01-money-and-gst-engine.md. It uses exact fractions and Python's
unbounded integers, where the Go engine uses 128-bit integer tricks. If the two
ever disagree, one of them has a bug; that is the point of having two.

Usage:
  python3 tools/golden/oracle.py check               # verify every golden file
  python3 tools/golden/oracle.py fill                # fill missing "expected" in cases.json
  python3 tools/golden/oracle.py parity --n 200 --seed 7   # regenerate parity.json

Only the standard library is used, so it runs anywhere Python 3.9+ does.
"""
import argparse
import json
import random
import sys
from fractions import Fraction
from math import floor
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
CASES = ROOT / "packages" / "golden" / "tax" / "cases.json"
PARITY = ROOT / "packages" / "golden" / "tax" / "parity.json"

BP = 10_000
MAX_LINES = 500
MAX_MODIFIERS = 50
MAX_QTY = 100_000
MAX_UNIT = 1_000_000_000
MAX_RATE = 4_000  # 40%, the top GST rate
MAX_SUBTOTAL = 100_000_000_000_000


class PricingError(Exception):
    def __init__(self, code):
        super().__init__(code)
        self.code = code


def round_half_up(x: Fraction) -> int:
    """Round a non-negative exact fraction to an integer, .5 going up."""
    assert x >= 0
    return floor(x + Fraction(1, 2))


def discount_on(amount: int, d) -> int:
    if not d:
        return 0
    pct = d.get("percent_bp", 0)
    amt = d.get("amount_paise", 0)
    if pct and amt:
        raise PricingError("bad_discount")
    if pct < 0 or pct > BP or amt < 0:
        raise PricingError("bad_discount")
    if amt > amount:
        raise PricingError("discount_exceeds_amount")
    if pct:
        return round_half_up(Fraction(amount * pct, BP))
    return amt


def allocate(total: int, weights):
    """Largest remainder; ties to the lower index."""
    s = sum(weights)
    if total == 0 or s == 0:
        return [0] * len(weights)
    exact = [Fraction(total * w, s) for w in weights]
    out = [floor(e) for e in exact]
    left = total - sum(out)
    order = sorted(range(len(weights)), key=lambda i: (-(exact[i] - out[i]), i))
    for i in order[:left]:
        out[i] += 1
    return out


def _is_int(v):
    # JSON integers only: Python would happily treat true as 1 and 1.0 as 1,
    # where the Go engine refuses them while decoding.
    return isinstance(v, int) and not isinstance(v, bool)


def _check_types(inp):
    ints = [inp.get("service_charge_bp", 0)]
    for d in (inp.get("bill_discount") or {},):
        ints += [d.get("percent_bp", 0), d.get("amount_paise", 0)]
    for l in inp.get("lines") or []:
        ints += [l.get("qty", 0), l.get("unit_price_paise", 0), l.get("tax_rate_bp", 0)]
        d = l.get("discount") or {}
        ints += [d.get("percent_bp", 0), d.get("amount_paise", 0)]
        ints += [m.get("price_paise", 0) for m in (l.get("modifiers") or [])]
    if not all(_is_int(v) for v in ints):
        raise PricingError("bad_json")


def validate(inp):
    _check_types(inp)
    if inp.get("price_mode") not in ("exclusive", "inclusive"):
        raise PricingError("bad_price_mode")
    if inp.get("supply") not in ("intra", "inter"):
        raise PricingError("bad_supply")
    if inp.get("rounding") not in ("nearest_rupee", "none"):
        raise PricingError("bad_rounding")
    sc = inp.get("service_charge_bp", 0)
    if sc < 0 or sc > BP:
        raise PricingError("bad_service_charge")
    lines = inp.get("lines") or []
    if not lines:
        raise PricingError("no_lines")
    if len(lines) > MAX_LINES:
        raise PricingError("too_many_lines")
    for l in lines:
        if not (1 <= l.get("qty", 0) <= MAX_QTY):
            raise PricingError("bad_qty")
        if not (0 <= l.get("unit_price_paise", 0) <= MAX_UNIT):
            raise PricingError("bad_price")
        if not (0 <= l.get("tax_rate_bp", 0) <= MAX_RATE):
            raise PricingError("bad_rate")
        mods = l.get("modifiers") or []
        if len(mods) > MAX_MODIFIERS:
            raise PricingError("bad_price")
        for m in mods:
            if not (0 <= m.get("price_paise", 0) <= MAX_UNIT):
                raise PricingError("bad_price")


def compute(inp):
    validate(inp)
    mode, supply = inp["price_mode"], inp["supply"]
    sc_bp = inp.get("service_charge_bp", 0)

    lines, after = [], []
    for l in inp["lines"]:
        unit = l.get("unit_price_paise", 0) + sum(m.get("price_paise", 0) for m in (l.get("modifiers") or []))
        gross = l["qty"] * unit
        ld = discount_on(gross, l.get("discount"))
        lines.append({"ref": l.get("ref", ""), "gross": gross, "line_discount": ld})
        after.append(gross - ld)
    subtotal = sum(x["gross"] for x in lines)
    if subtotal > MAX_SUBTOTAL:
        raise PricingError("amount_too_large")

    bill_disc = discount_on(sum(after), inp.get("bill_discount"))
    shares = allocate(bill_disc, after)
    for x, a, s in zip(lines, after, shares):
        x["bill_discount"] = s
        x["net"] = a - s

    by_rate = {}
    for l, x in zip(inp["lines"], lines):
        rate = l.get("tax_rate_bp", 0)
        by_rate[rate] = by_rate.get(rate, 0) + x["net"]

    groups = []
    for rate in sorted(by_rate):
        net = by_rate[rate]
        r = Fraction(rate, BP)
        g_cgst = g_sgst = g_igst = 0
        if mode == "inclusive":
            base0 = round_half_up(Fraction(net) / (1 + r))
            if supply == "intra":
                g_cgst = g_sgst = round_half_up(base0 * r / 2)
            else:
                g_igst = round_half_up(base0 * r)
            goods = net - g_cgst - g_sgst - g_igst
        else:
            goods = net
        sc = round_half_up(goods * Fraction(sc_bp, BP))
        taxable = goods + sc
        if mode == "inclusive":
            if supply == "intra":
                h = round_half_up(sc * r / 2)
                cgst, sgst, igst = g_cgst + h, g_sgst + h, 0
            else:
                cgst, sgst, igst = 0, 0, g_igst + round_half_up(sc * r)
        else:
            if supply == "intra":
                cgst = sgst = round_half_up(taxable * r / 2)
                igst = 0
            else:
                cgst = sgst = 0
                igst = round_half_up(taxable * r)
        groups.append({
            "rate_bp": rate, "net": net, "goods_value": goods, "service_charge": sc,
            "taxable": taxable, "cgst": cgst, "sgst": sgst, "igst": igst,
        })

    cgst = sum(g["cgst"] for g in groups)
    sgst = sum(g["sgst"] for g in groups)
    igst = sum(g["igst"] for g in groups)
    taxable_total = sum(g["taxable"] for g in groups)
    total = taxable_total + cgst + sgst + igst
    round_off = 0
    if inp["rounding"] == "nearest_rupee":
        # nearest multiple of 100 paise, 50 going up
        round_off = round_half_up(Fraction(total, 100)) * 100 - total
    return {
        "lines": lines,
        "subtotal": subtotal,
        "discount_total": sum(x["line_discount"] + x["bill_discount"] for x in lines),
        "service_charge": sum(g["service_charge"] for g in groups),
        "taxable_total": taxable_total,
        "tax_groups": groups,
        "cgst": cgst, "sgst": sgst, "igst": igst,
        "tax_total": cgst + sgst + igst,
        "round_off": round_off,
        "grand_total": total + round_off,
    }


def run_case(case):
    try:
        return {"result": compute(case["input"])}
    except PricingError as e:
        return {"error": e.code}


def load(path):
    return json.loads(path.read_text())


def dump(path, doc):
    path.write_text(json.dumps(doc, indent=2, ensure_ascii=False) + "\n")


def check():
    bad = 0
    for path in (CASES, PARITY):
        if not path.exists():
            continue
        doc = load(path)
        for c in doc["cases"]:
            got = run_case(c)
            if got != c.get("expected"):
                bad += 1
                print(f"MISMATCH {path.name}: {c['name']}")
                print("  expected:", json.dumps(c.get("expected"))[:400])
                print("  oracle:  ", json.dumps(got)[:400])
        print(f"{path.name}: {len(doc['cases'])} cases checked")
    if bad:
        print(f"{bad} mismatches")
        sys.exit(1)
    print("oracle agrees with every golden case")


def fill():
    doc = load(CASES)
    n = 0
    for c in doc["cases"]:
        if "expected" not in c:
            c["expected"] = run_case(c)
            n += 1
    dump(CASES, doc)
    print(f"filled {n} cases")


def random_input(rng):
    mode = rng.choice(["exclusive", "inclusive"])
    supply = rng.choice(["intra"] * 4 + ["inter"])
    rates = [0, 500, 500, 500, 1200, 1800, 2800, 4000]
    lines = []
    for i in range(rng.randint(1, 12)):
        l = {
            "ref": f"L{i + 1}",
            "name": f"item {i + 1}",
            "qty": rng.choice([1, 1, 1, 2, 2, 3, 4, 6, 10, 25]),
            "unit_price_paise": rng.choice([0, 1, 99, 1000, 4999, 9900, 12500, 19900, 29999, 45000, 120000, rng.randint(1, 500000)]),
            "tax_rate_bp": rng.choice(rates),
        }
        if rng.random() < 0.25:
            l["modifiers"] = [{"name": "add-on", "price_paise": rng.choice([500, 2000, 3999, 4000])}
                              for _ in range(rng.randint(1, 3))]
        if rng.random() < 0.15:
            l["discount"] = {"percent_bp": rng.choice([500, 1000, 1250, 3333, 5000, 10000])}
        lines.append(l)
    inp = {"price_mode": mode, "supply": supply,
           "rounding": rng.choice(["nearest_rupee", "nearest_rupee", "none"]), "lines": lines}
    r = rng.random()
    if r < 0.2:
        inp["bill_discount"] = {"percent_bp": rng.choice([500, 1000, 1500, 2000, 3333])}
    elif r < 0.35:
        inp["bill_discount"] = {"amount_paise": rng.choice([100, 1000, 2500, 5000, 9999])}
    if rng.random() < 0.2:
        inp["service_charge_bp"] = rng.choice([500, 750, 1000])
    return inp


def parity(n, seed):
    rng = random.Random(seed)
    cases = []
    while len(cases) < n:
        inp = random_input(rng)
        case = {"name": f"parity-{seed}-{len(cases) + 1:04d}", "input": inp}
        case["expected"] = run_case(case)
        cases.append(case)
    about = ("Machine-generated parity cases (seed %d). They prove the Go, Dart and Python "
             "engines agree with each other; they are NOT checked by a chartered accountant. "
             "Regenerate with: python3 tools/golden/oracle.py parity --n %d --seed %d") % (seed, n, seed)
    # One case per line keeps the file small and its git diffs readable.
    body = ",\n".join(json.dumps(c, ensure_ascii=False, separators=(",", ":")) for c in cases)
    PARITY.write_text('{"version":1,"about":%s,"cases":[\n%s\n]}\n' % (json.dumps(about), body))
    print(f"wrote {n} parity cases")


if __name__ == "__main__":
    ap = argparse.ArgumentParser()
    sub = ap.add_subparsers(dest="cmd", required=True)
    sub.add_parser("check")
    sub.add_parser("fill")
    p = sub.add_parser("parity")
    p.add_argument("--n", type=int, default=200)
    p.add_argument("--seed", type=int, default=7)
    a = ap.parse_args()
    {"check": check, "fill": fill}.get(a.cmd, lambda: parity(a.n, a.seed))()
