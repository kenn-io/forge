package settingsservertest

import (
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/stretchr/testify/assert"
	"go.kenn.io/forge/internal/server/archiveapi"
	serverfake "go.kenn.io/forge/internal/testutil/serverfake"
)

func TestArchiveReportResponseTransformSchemaNamesReportSchema(t *testing.T) {
	serverfake.RunParallelServerTest(t)

	property := &huma.Schema{}
	schema := &huma.Schema{
		Properties: map[string]*huma.Schema{"schema": property},
	}

	response := archiveapi.ArchiveReportResponse{}
	assert.Same(t, schema, response.TransformSchema(nil, schema))
	assert.Equal(t, "ReportSchema", property.Extensions["x-go-name"])
}
