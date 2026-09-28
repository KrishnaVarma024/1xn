package pricing

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// goldenDir is packages/golden/tax at the repo root. The device (Dart) and
// the oracle (Python) read the same files.
var goldenDir = filepath.Join("..", "..", "..", "..", "..", "packages", "golden", "tax")

type goldenFile struct {
	Cases []struct {
		Name     string          `json:"name"`
		Input    Input           `json:"input"`
		Expected json.RawMessage `json:"expected"`
	} `json:"cases"`
}

// TestGolden runs every hand-written case and every parity case and demands
// an exact match, field by field, paisa by paisa.
func TestGolden(t *testing.T) {
	for _, file := range []string{"cases.json", "parity.json"} {
		raw, err := os.ReadFile(filepath.Join(goldenDir, file))
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		var gf goldenFile
		if err := json.Unmarshal(raw, &gf); err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		if len(gf.Cases) == 0 {
			t.Fatalf("%s has no cases", file)
		}
		for _, c := range gf.Cases {
			c := c
			t.Run(c.Name, func(t *testing.T) {
				got := outcome(c.Input)
				var want, have any
				if err := json.Unmarshal(c.Expected, &want); err != nil {
					t.Fatalf("expected is not JSON: %v", err)
				}
				b, _ := json.Marshal(got)
				_ = json.Unmarshal(b, &have)
				if !reflect.DeepEqual(have, want) {
					t.Errorf("mismatch\n got: %s\nwant: %s", b, c.Expected)
				}
			})
		}
	}
}

// outcome mirrors the oracle's output shape: {"result": ...} or {"error": code}.
func outcome(in Input) map[string]any {
	res, err := Compute(in)
	if err != nil {
		var pe *Error
		if errors.As(err, &pe) {
			return map[string]any{"error": pe.Code}
		}
		return map[string]any{"error": err.Error()}
	}
	return map[string]any{"result": res}
}
