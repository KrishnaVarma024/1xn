# Golden test cases

The contract between every implementation of the pricing and GST engine: Go on the server, Dart on the device, Python as the reference oracle.

| File | What | Trust level |
| --- | --- | --- |
| `tax/cases.json` | 27 hand-written cases, each with a `why` | Hand-checked against the rules in `docs/learn/01-money-and-gst-engine.md`. **To be signed off by a chartered accountant before pilot**, then grown to 500 real bills. |
| `tax/parity.json` | 200 generated cases | Proves the engines agree with each other. Not proof that they are right. |

Each case has an `input` and an `expected`, which is either `{"result": {...}}` or `{"error": "<code>"}`. All amounts are paise; rates are basis points.

```bash
python3 tools/golden/oracle.py check                       # oracle vs both files
python3 tools/golden/oracle.py fill                        # compute "expected" for new cases in cases.json
python3 tools/golden/oracle.py parity --n 200 --seed 7     # regenerate parity.json
```

Adding a case: write its `input` and `why` in `cases.json` with no `expected`, then work the numbers out by hand. Run `fill` and compare the oracle's answer with yours before you commit. If they differ, find out which one is wrong. That is the point of the exercise.
