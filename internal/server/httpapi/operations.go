package httpapi

import "github.com/danielgtaylor/huma/v2"

// DocumentOperation applies the metadata required by the checked-in OpenAPI
// contract to routes registered with Huma's convenience helpers.
func DocumentOperation(
	operationID, summary string,
	tags ...string,
) func(*huma.Operation) {
	return func(operation *huma.Operation) {
		operation.OperationID = operationID
		operation.Summary = summary
		operation.Tags = tags
	}
}
