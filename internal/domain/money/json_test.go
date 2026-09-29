package money_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	"github.com/KaioVinicios/pda/internal/domain/money"
)

func TestMoneyJSON(t *testing.T) {
	// Covers: TST-U01, MON-01, MON-04, MON-09
	marshal := map[int64]string{
		2500: `{"amount":"25.00","currency":"BRL"}`,
		0:    `{"amount":"0.00","currency":"BRL"}`,
		-500: `{"amount":"-5.00","currency":"BRL"}`,
	}
	for v, want := range marshal {
		got, err := json.Marshal(minor(t, v, money.BRL))
		if err != nil || string(got) != want {
			t.Errorf("Marshal(%d) = %s, %v; want %s", v, got, err, want)
		}
	}

	if _, err := json.Marshal(money.Money{}); !errors.Is(err, money.ErrUninitialized) {
		t.Errorf("Marshal(Money{}) error = %v, want ErrUninitialized", err)
	}

	var m money.Money
	if err := json.Unmarshal([]byte(`{"amount":"25.00","currency":"BRL"}`), &m); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if m != minor(t, 2500, money.BRL) {
		t.Fatalf("Unmarshal = %v %s, want 25.00 BRL", m, m.Currency())
	}

	invalid := map[string]error{
		`{"amount":"-1.00","currency":"BRL"}`:      money.ErrInvalidAmount,
		`{"amount":25.00,"currency":"BRL"}`:        money.ErrInvalidAmount,
		`{"amount":"25","currency":"BRL"}`:         money.ErrInvalidAmount,
		`{"currency":"BRL"}`:                       money.ErrInvalidAmount,
		`{"amount":"1.00"}`:                        money.ErrInvalidCurrency,
		`{"amount":"1.00","currency":"brl"}`:       money.ErrInvalidCurrency,
		`{"amount":"1.00","currency":"BRL","x":1}`: money.ErrInvalidAmount,
		`null`:                              money.ErrInvalidAmount,
		`{"amount":null,"currency":"BRL"}`:  money.ErrInvalidAmount,
		`{"amount":"1.00","currency":null}`: money.ErrInvalidCurrency,
		`"25.00"`:                           money.ErrInvalidAmount,
	}
	for in, want := range invalid {
		var got money.Money
		if err := json.Unmarshal([]byte(in), &got); !errors.Is(err, want) {
			t.Errorf("Unmarshal(%s) error = %v, want %v", in, err, want)
		}
	}

	// Inside a strict outer decoder the rules still hold.
	var body struct {
		Money money.Money `json:"money"`
	}
	dec := json.NewDecoder(bytes.NewReader([]byte(`{"money":{"amount":"1.00","currency":"BRL","extra":true}}`)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err == nil {
		t.Error("unknown field inside money must be rejected")
	}
}
