package platform

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// DecodeEvidencePage charges each provider record before decoding it. Size is
// the requested provider page limit, not evidence of inventory completeness.
func DecodeEvidencePage[T any](data []byte, size int, meter *Meter) ([]T, error) {
	decoder := jsontext.NewDecoder(bytes.NewReader(data))
	start, err := decoder.ReadToken()
	if err != nil || start.Kind() != '[' {
		return nil, &Error{Code: ErrCodeProviderContract, Field: "page_items"}
	}
	items := make([]T, 0)
	for decoder.PeekKind() != ']' {
		if len(items) >= size {
			return nil, &Error{Code: ErrCodeProviderContract, Field: "page_size"}
		}
		if err := meter.Records(1); err != nil {
			return nil, err
		}
		var item T
		if err := json.UnmarshalDecode(decoder, &item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if _, err := decoder.ReadToken(); err != nil {
		return nil, err
	}
	if _, err := decoder.ReadToken(); err != io.EOF {
		return nil, &Error{Code: ErrCodeProviderContract, Field: "page_trailing_data"}
	}
	return items, nil
}

// NextEvidencePage validates Link progression without following a supplied URL.
// The provider adapter constructs the next request using its original scope.
func NextEvidencePage(header http.Header, current int) (bool, error) {
	found := false
	for part := range strings.SplitSeq(header.Get("Link"), ",") {
		link, params, ok := strings.Cut(strings.TrimSpace(part), ";")
		if !ok {
			continue
		}
		_, attributes, err := mime.ParseMediaType("link;" + params)
		if err != nil {
			return false, &Error{Code: ErrCodeProviderContract, Field: "page_link"}
		}
		if attributes["rel"] != "next" {
			continue
		}
		u, err := url.Parse(strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(link), "<"), ">"))
		if err != nil {
			return false, &Error{Code: ErrCodeProviderContract, Field: "page_link"}
		}
		page, err := strconv.Atoi(u.Query().Get("page"))
		if err != nil || page <= current || page-current != 1 || found {
			return false, &Error{Code: ErrCodeProviderContract, Field: "page_progress"}
		}
		found = true
	}
	return found, nil
}

// ObserveSHA retains missing, null/empty, and populated provider fields.
func ObserveSHA(value jsontext.Value) (SHAField, error) {
	field := SHAField{Present: len(value) > 0}
	if len(value) == 0 || string(value) == "null" {
		return field, nil
	}
	if err := json.Unmarshal(value, &field.Value); err != nil {
		return field, err
	}
	return field, nil
}

// PublishLandingSnapshot enforces the output envelope after partial collection.
// The caller may retain facts already admitted even when later input is capped.
func PublishLandingSnapshot(ctx context.Context, snapshot LandingSnapshot, meter *Meter) (LandingSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return LandingSnapshot{}, err
	}
	if err := meter.CheckOutput(snapshot); err != nil {
		return LandingSnapshot{}, err
	}
	return snapshot, nil
}
