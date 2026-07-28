package server

import (
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/stretchr/testify/assert"
)

func TestArchiveReportResponseTransformSchemaHandlesMissingSchemaProperty(t *testing.T) {
	t.Parallel()

	response := archiveReportResponse{}
	assert.Nil(t, response.TransformSchema(nil, nil))

	schema := &huma.Schema{}
	assert.Same(t, schema, response.TransformSchema(nil, schema))

	schema.Properties = map[string]*huma.Schema{"schema": nil}
	assert.Same(t, schema, response.TransformSchema(nil, schema))
}

func TestArchiveReportResponseTransformSchemaNamesReportSchema(t *testing.T) {
	t.Parallel()

	property := &huma.Schema{}
	schema := &huma.Schema{
		Properties: map[string]*huma.Schema{"schema": property},
	}

	response := archiveReportResponse{}
	assert.Same(t, schema, response.TransformSchema(nil, schema))
	assert.Equal(t, "ReportSchema", property.Extensions["x-go-name"])
}
