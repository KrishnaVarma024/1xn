# 01 Money and the GST engine

**This note is the spec.** The Go engine (`modules/sales/pricing`), the Dart engine (device, next slice) and the Python oracle (`tools/golden/oracle.py`) all implement exactly these rules. To change a rule: change this note, update `packages/golden/tax/cases.json`, and make all three engines pass.

## Why integer paise

In floating point, `0.1 + 0.2 == 0.30000000000000004`. Add that error across a month of bills and the GST return disagrees with the books. So every amount is an **integer number of paise** (`int64`) and every rate is an **integer number of basis points** (500 = 5%). The only division in the engine is `mulDivRound(a, b, c)`, which computes `a × b ÷ c` with a 128-bit intermediate (so `a × b` cannot overflow) and rounds **half up**: 0.5 paise and above goes up.

## The rules, in order

The order matters. Doing the same steps in a different order changes paise.

**R0. Limits.** Rates are 0 to 4,000 bp (0% to 40%, the top GST rate), quantities 1 to 100,000, unit prices up to ₹1 crore, bills up to ₹1,000 crore. The limits keep every product inside 64 bits. The rate cap also keeps inclusive back-calculation sane: at an absurd 100% rate, a one-paisa line would produce a negative goods value.

**R1. Gross.** For each line: `gross = qty × (unit price + sum of modifier prices)`. Modifiers take the line's tax rate.

**R2. Line discount.** A line may have a percent discount (`percent_bp`, rounded half up) or a fixed amount (`amount_paise`, never more than the line). Not both.

**R3. Bill discount, split fairly.** The bill discount (percent of the post-line-discount total, or a fixed amount) is split across lines in proportion to their value, using the **largest-remainder method**:
1. each line gets `floor(discount × line ÷ total)`;
2. the leftover paise go one each to the lines with the largest remainders;
3. **ties go to the lower line index**. Every engine must break ties the same way, or they disagree by a paisa.

**R4. Group by rate.** Lines are grouped by GST rate, lowest rate first. Tax is computed **once per group**, not per line.

**R5. Tax per group.** CGST and SGST are two separate taxes. Each is computed and rounded **on its own**: `half = round(taxable × rate ÷ 2)`, CGST = SGST = half. So they are always equal. IGST (inter-state) is `round(taxable × rate)`.

**R6. Inclusive prices.** When the menu price includes GST, the guest must pay exactly the menu price:
1. `base0 = round(net ÷ (1 + rate))`;
2. `half = round(base0 × rate ÷ 2)` (or `igst = round(base0 × rate)`);
3. `goods value = net − 2 × half`. The goods value absorbs the rounding, so goods + tax = net exactly.

**R7. Service charge.** Service charge is off by default. The consumer-protection guidelines forbid adding it automatically, so it is only added when the guest agrees. When it is charged, it is part of the value of supply and is taxed at the group's rate: `sc = round(goods value × sc%)`, `taxable = goods value + sc`. With exclusive prices, tax is computed on `taxable`. With inclusive prices, the goods tax from R6 stays as it is, and only the service charge is taxed forward.

**R8. Round-off.** With `nearest_rupee`, the grand total is rounded to whole rupees, 50 paise going up, and the difference (−49 to +50 paise) is shown as a round-off line. With `none`, paise stay.

## Worked by hand

**₹95 pav bhaji at 5%, exclusive** (`halves-rounded-separately-then-round-off-up`)
- CGST = 9,500 × 2.5% = 237.5 paise, rounded up to **238**. SGST is the same, **238**.
- Tax = 4.76, not 4.75, because each tax is rounded on its own (R5). That is correct: they are two taxes.
- Total 9,976 paise. Rounded to the rupee: 10,000, round-off **+24**.

**₹99 cold coffee, inclusive** (`inclusive-menu-price-preserved`)
- base0 = 9,900 ÷ 1.05 = 9,428.57, rounded to **9,429**.
- half = 9,429 × 2.5% = 235.725, rounded to **236**.
- goods value = 9,900 − 472 = **9,428**. Guest pays exactly ₹99.00.

**₹10 off three ₹100 lines** (`bill-amount-discount-remainder`)
- Exact share: 333.33 paise each. floor gives 333 × 3 = 999, with 1 paisa left over.
- All three remainders tie, so the first line gets it: **334, 333, 333**. Nets: 9,666 / 9,667 / 9,667.

## Why tax per group, not per line?

Rounding per line adds up to half a paisa of error per line. On a 12-line bill that can be 6 paise off the "true" tax. Rounding per rate group bounds it at half a paisa per tax head per rate. It also matches how GSTR-1 reports totals: rate-wise taxable value and tax.

## What would break at 10x?

Nothing in the maths. It is pure and fast: about 1.6 µs for an 8-line bill on one core of the build machine, roughly 600,000 bills a second. Run `go test ./modules/sales/pricing -bench .` to measure on your Mac. The risk is **drift between engines**. That is why the golden files exist, and why they run in CI for every change.

## Interview questions

<details><summary>1. Why not store money as a DECIMAL/float?</summary>
Floats cannot represent most decimal fractions, so errors accumulate. DECIMAL is fine in Postgres and Go, but awkward in JSON and Dart. Integer minor units work the same in every language and are exact.</details>

<details><summary>2. Split ₹100 across three people "fairly" to the paisa. How?</summary>
Largest remainder: 33.33 each floored to 3,333 paise; the 1 paisa left over goes to the largest remainder. Say how ties are broken. That is the part people forget, and the part that makes two systems disagree.</details>

<details><summary>3. Two engines (server and device) must agree forever. How do you guarantee it?</summary>
Write a spec. Hand-write golden cases. Generate parity cases. Run every engine against them in CI. Add an independent oracle that is implemented differently (here: exact fractions instead of 128-bit integer tricks).</details>

<details><summary>4. Where can int64 overflow in money code, and how do you prevent it?</summary>
In products like amount × rate or discount × weight. Put limits on the inputs, and use a 128-bit intermediate for multiply-then-divide (`math/bits.Mul64`, `Div64`).</details>

<details><summary>5. Menu says ₹99 including GST. How do you compute the tax without the guest paying ₹99.01?</summary>
Back-calculate a base, compute the tax on it, and let the goods value absorb the rounding. goods = net − tax, so the total is exact by construction (rule R6).</details>

## Exercises

1. Add a golden case: 3 × ₹33.33 at 18% inclusive, with a 7% bill discount. First predict every number by hand. Then run `python3 tools/golden/oracle.py fill` and compare.
2. Change the tie-break in `allocate` to "higher index wins" and run `make test`. Count how many golden cases fail and why.
3. Run `make fuzz` for five minutes. Read `FuzzCompute` and add one more invariant it should check.
