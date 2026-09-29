package money_test

import (
	"errors"
	"math"
	"testing"

	"github.com/KaioVinicios/pda/internal/domain/money"
)

func minor(t *testing.T, v int64, c money.Currency) money.Money {
	t.Helper()
	m, err := money.FromMinor(v, c)
	if err != nil {
		t.Fatalf("FromMinor(%d, %s): %v", v, c, err)
	}
	return m
}

func TestParseMoney(t *testing.T) {
	// Covers: TST-U01, MON-03, MON-04, MON-05, MON-08
	valid := map[string]int64{
		"0.00":                 0,
		"0.01":                 1,
		"25.00":                2500,
		"1000.10":              100010,
		"92233720368547758.07": math.MaxInt64,
	}
	for in, want := range valid {
		m, err := money.Parse(in, "BRL")
		if err != nil || m.Minor() != want || m.Currency() != money.BRL {
			t.Errorf("Parse(%q) = %d %s, %v; want %d BRL", in, m.Minor(), m.Currency(), err, want)
		}
		if m.String() != in {
			t.Errorf("Parse(%q).String() = %q", in, m.String())
		}
	}

	invalidAmounts := []string{
		"", "25", "25.0", "25.", ".50", "025.00", "00.00", "+1.00", "-1.00", "1e3",
		"1E3", "NaN", "Infinity", "1.001", " 1.00", "1.00 ", "1,00", "1_000.00", "١.٠٠",
	}
	for _, in := range invalidAmounts {
		if _, err := money.Parse(in, "BRL"); !errors.Is(err, money.ErrInvalidAmount) {
			t.Errorf("Parse(%q) error = %v, want ErrInvalidAmount", in, err)
		}
	}

	for _, in := range []string{"92233720368547758.08", "99999999999999999999.00"} {
		_, err := money.Parse(in, "BRL")
		if !errors.Is(err, money.ErrInvalidAmount) || !errors.Is(err, money.ErrOverflow) {
			t.Errorf("Parse(%q) error = %v, want ErrInvalidAmount and ErrOverflow", in, err)
		}
	}

	for _, c := range []string{"", "brl", "BRLX", "JPY", " BRL"} {
		if _, err := money.Parse("1.00", c); !errors.Is(err, money.ErrInvalidCurrency) {
			t.Errorf("Parse(1.00, %q) error = %v, want ErrInvalidCurrency", c, err)
		}
	}

	if _, err := money.Parse("1e3", "brl"); !errors.Is(err, money.ErrInvalidAmount) {
		t.Errorf("amount must be checked before currency, got %v", err)
	}
}

func TestParseCurrency(t *testing.T) {
	// Covers: TST-U01, MON-04, DOM-03
	for _, s := range []string{"BRL", "USD", "EUR"} {
		c, err := money.ParseCurrency(s)
		if err != nil || string(c) != s || !c.Valid() {
			t.Errorf("ParseCurrency(%q) = %q, %v", s, c, err)
		}
	}
	for _, s := range []string{"", "brl", "JPY", "BRL "} {
		if _, err := money.ParseCurrency(s); !errors.Is(err, money.ErrInvalidCurrency) {
			t.Errorf("ParseCurrency(%q) error = %v, want ErrInvalidCurrency", s, err)
		}
	}
	if money.Currency("").Valid() {
		t.Error(`Currency("") must be invalid`)
	}
}

func TestMoneyArithmetic(t *testing.T) {
	// Covers: TST-U01, MON-02, MON-08, MON-09
	a, b := minor(t, 2500, money.BRL), minor(t, 1000, money.BRL)

	sum, err := a.Add(b)
	if err != nil || sum.Minor() != 3500 {
		t.Fatalf("25.00 + 10.00 = %v, %v", sum, err)
	}
	diff, err := b.Sub(a)
	if err != nil || diff.Minor() != -1500 || diff.String() != "-15.00" {
		t.Fatalf("10.00 - 25.00 = %v, %v", diff, err)
	}
	neg, err := a.Negate()
	if err != nil || neg.Minor() != -2500 {
		t.Fatalf("-(25.00) = %v, %v", neg, err)
	}
	if a.Minor() != 2500 || b.Minor() != 1000 {
		t.Fatal("operations must not change their operands")
	}

	cmps := []struct {
		x, y money.Money
		want int
	}{{a, b, 1}, {b, a, -1}, {a, a, 0}}
	for _, c := range cmps {
		if got, err := c.x.Cmp(c.y); err != nil || got != c.want {
			t.Errorf("Cmp(%v, %v) = %d, %v; want %d", c.x, c.y, got, err, c.want)
		}
	}

	signs := map[int64]int{-1: -1, 0: 0, 1: 1}
	for v, want := range signs {
		if got := minor(t, v, money.BRL).Sign(); got != want {
			t.Errorf("Sign(%d) = %d, want %d", v, got, want)
		}
	}

	maxM, minM := minor(t, math.MaxInt64, money.BRL), minor(t, math.MinInt64, money.BRL)
	one, minusOne := minor(t, 1, money.BRL), minor(t, -1, money.BRL)
	overflows := map[string]func() (money.Money, error){
		"max + 1":     func() (money.Money, error) { return maxM.Add(one) },
		"min + (-1)":  func() (money.Money, error) { return minM.Add(minusOne) },
		"min - 1":     func() (money.Money, error) { return minM.Sub(one) },
		"max - (-1)":  func() (money.Money, error) { return maxM.Sub(minusOne) },
		"-(minInt64)": minM.Negate,
	}
	for name, op := range overflows {
		if _, err := op(); !errors.Is(err, money.ErrOverflow) {
			t.Errorf("%s: error = %v, want ErrOverflow", name, err)
		}
	}
	if minM.String() != "-92233720368547758.08" {
		t.Errorf("min String() = %q", minM.String())
	}
}

func TestMoneyCurrencyMismatch(t *testing.T) {
	// Covers: TST-U01, MON-07, MON-12
	brl, usd := minor(t, 100, money.BRL), minor(t, 100, money.USD)
	if _, err := brl.Add(usd); !errors.Is(err, money.ErrCurrencyMismatch) {
		t.Errorf("Add: %v", err)
	}
	if _, err := brl.Sub(usd); !errors.Is(err, money.ErrCurrencyMismatch) {
		t.Errorf("Sub: %v", err)
	}
	if _, err := brl.Cmp(usd); !errors.Is(err, money.ErrCurrencyMismatch) {
		t.Errorf("Cmp: %v", err)
	}
}

func TestMoneyZeroValue(t *testing.T) {
	// Covers: TST-U01, MON-11, DOM-03
	var z money.Money
	v := minor(t, 100, money.BRL)
	ops := map[string]func() error{
		"zero.Add":    func() error { _, err := z.Add(v); return err },
		"value.Add":   func() error { _, err := v.Add(z); return err },
		"zero.Sub":    func() error { _, err := z.Sub(v); return err },
		"value.Sub":   func() error { _, err := v.Sub(z); return err },
		"zero.Cmp":    func() error { _, err := z.Cmp(v); return err },
		"value.Cmp":   func() error { _, err := v.Cmp(z); return err },
		"zero.Negate": func() error { _, err := z.Negate(); return err },
	}
	for name, op := range ops {
		if err := op(); !errors.Is(err, money.ErrUninitialized) {
			t.Errorf("%s: error = %v, want ErrUninitialized", name, err)
		}
	}
	if _, err := money.Zero(""); !errors.Is(err, money.ErrInvalidCurrency) {
		t.Errorf(`Zero(""): %v`, err)
	}
	if _, err := money.FromMinor(1, ""); !errors.Is(err, money.ErrInvalidCurrency) {
		t.Errorf(`FromMinor(1, ""): %v`, err)
	}
	if z.String() != "" || z.Sign() != 0 {
		t.Errorf("zero value reads: %q %d", z.String(), z.Sign())
	}
}
