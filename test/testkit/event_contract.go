package testkit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/KaioVinicios/pda/api"
)

// EventContract validates the integration events against api/events.yaml
// (spec M4, decision 16): a message off the contract fails the test that
// received it.
type EventContract struct {
	schemas openapi3.Schemas
}

// LoadEventContract loads and validates the embedded document.
func LoadEventContract(ctx context.Context) (*EventContract, error) {
	doc, err := openapi3.NewLoader().LoadFromData(api.Events)
	if err != nil {
		return nil, fmt.Errorf("testkit: load the event contract: %w", err)
	}
	if err := doc.Validate(ctx); err != nil {
		return nil, fmt.Errorf("testkit: invalid event contract: %w", err)
	}
	return &EventContract{schemas: doc.Components.Schemas}, nil
}

// Validate checks one message body: the envelope against Envelope and data
// against <eventType>V<version>. Numbers are decoded as json.Number, never as
// floating point.
func (c *EventContract) Validate(body []byte) error {
	var msg map[string]any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&msg); err != nil {
		return fmt.Errorf("event contract: body is not a JSON object: %w", err)
	}
	if dec.More() {
		return errors.New("event contract: data after the JSON object")
	}
	if err := c.schemas["Envelope"].Value.VisitJSON(msg); err != nil {
		return fmt.Errorf("event contract: envelope: %s", firstLine(err))
	}
	name := fmt.Sprintf("%sV%s", msg["eventType"], msg["version"])
	schema, ok := c.schemas[name]
	if !ok {
		return fmt.Errorf("event contract: no schema %s", name)
	}
	if err := schema.Value.VisitJSON(msg["data"]); err != nil {
		return fmt.Errorf("event contract: %s: %s", name, firstLine(err))
	}
	return nil
}
