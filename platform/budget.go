package platform

import (
	"context"
	"encoding/json/v2"
	"io"
	"math"
	"net/http"
)

// Budget is the caller's finite evidence envelope. There is no unlimited zero
// value; the operation context must also carry a deadline.
type Budget struct {
	MaxRecords     int64 `json:"max_records" minimum:"1"`
	MaxNodes       int64 `json:"max_nodes" minimum:"1"`
	MaxBytes       int64 `json:"max_bytes" minimum:"1"`
	MaxOutputBytes int64 `json:"max_output_bytes" minimum:"1"`
}

// Meter belongs to one sequential analysis or provider read. It is not a quota
// pool; applications retain ownership of admission between independent callers.
type Meter struct {
	records, nodes, bytes, output int64
}

func NewMeter(ctx context.Context, budget Budget) (*Meter, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, ok := ctx.Deadline(); !ok {
		return nil, &Error{Code: ErrCodeInvalidArgument, Field: "deadline"}
	}
	if budget.MaxRecords <= 0 || budget.MaxNodes <= 0 || budget.MaxBytes <= 0 || budget.MaxOutputBytes <= 0 || budget.MaxBytes == math.MaxInt64 {
		return nil, &Error{Code: ErrCodeInvalidArgument, Field: "budget"}
	}
	return &Meter{records: budget.MaxRecords, nodes: budget.MaxNodes, bytes: budget.MaxBytes, output: budget.MaxOutputBytes}, nil
}

func charge(remaining *int64, count int64, field string) error {
	if count < 0 {
		return &Error{Code: ErrCodeInvalidArgument, Field: field}
	}
	if count > *remaining {
		return &Error{Code: ErrCodePageLimit, Field: field}
	}
	*remaining -= count
	return nil
}

func (m *Meter) Records(count int64) error { return charge(&m.records, count, "records") }
func (m *Meter) Nodes(count int64) error   { return charge(&m.nodes, count, "nodes") }
func (m *Meter) Bytes(count int64) error   { return charge(&m.bytes, count, "decoded_bytes") }
func (m *Meter) Output(count int64) error  { return charge(&m.output, count, "output_bytes") }
func (m *Meter) RemainingRecords() int64   { return m.records }
func (m *Meter) RemainingNodes() int64     { return m.nodes }
func (m *Meter) RemainingBytes() int64     { return m.bytes }

// CheckOutput charges the encoded envelope without allocating a second copy
// of the whole response. The response owner controls eventual publication.
func (m *Meter) CheckOutput(value any) error {
	return json.MarshalWrite(outputMeter{m}, value)
}

type outputMeter struct{ meter *Meter }

func (w outputMeter) Write(data []byte) (int, error) {
	if err := w.meter.Output(int64(len(data))); err != nil {
		return 0, err
	}
	return len(data), nil
}

// Read bounds input allocation before reading, allowing one sentinel byte to
// distinguish an exactly full body from a truncated one. The underlying reader
// must honor cancellation (HTTP bodies and Git command pipes do).
func (m *Meter) Read(ctx context.Context, reader io.Reader) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(reader, m.bytes+1))
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := m.Bytes(int64(len(data))); err != nil {
		return nil, err
	}
	return data, nil
}

// ReadHTTP bounds decoded response bytes, including error bodies, before a
// provider's SDK or JSON decoder can allocate from them. It closes the body;
// the returned response carries status and headers for provider classification.
func (m *Meter) ReadHTTP(ctx context.Context, client *http.Client, request *http.Request) (*http.Response, []byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	response, err := client.Do(request.WithContext(ctx))
	if err != nil {
		return nil, nil, err
	}
	defer response.Body.Close()
	body, err := m.Read(ctx, response.Body)
	return response, body, err
}
