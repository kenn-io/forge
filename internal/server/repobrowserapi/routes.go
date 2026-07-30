package repobrowserapi

import (
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"go.kenn.io/forge/internal/server/httpapi"
)

func (h *Handler) Register(api huma.API) {
	repoPath := "/repo/{provider}/{owner}/{name}"
	hostRepoPath := "/host/{platform_host}/repo/{provider}/{owner}/{name}"

	huma.Get(api, repoPath+"/browser/refs", h.listRepoBrowserRefs,
		httpapi.DocumentOperation("list-repo-browser-refs", "List repository browser refs", "Repositories"))
	huma.Get(api, hostRepoPath+"/browser/refs", h.listRepoBrowserRefsOnHost,
		httpapi.DocumentOperation("list-repo-browser-refs-on-host", "List repository browser refs", "Repositories"))
	huma.Get(api, repoPath+"/browser/tree", h.listRepoBrowserTree,
		httpapi.DocumentOperation("list-repo-browser-tree", "List repository browser tree", "Repositories"))
	huma.Get(api, hostRepoPath+"/browser/tree", h.listRepoBrowserTreeOnHost,
		httpapi.DocumentOperation("list-repo-browser-tree-on-host", "List repository browser tree", "Repositories"))
	huma.Get(api, repoPath+"/browser/blob", h.getRepoBrowserBlob,
		httpapi.DocumentOperation("get-repo-browser-blob", "Get repository browser blob", "Repositories"))
	huma.Get(api, hostRepoPath+"/browser/blob", h.getRepoBrowserBlobOnHost,
		httpapi.DocumentOperation("get-repo-browser-blob-on-host", "Get repository browser blob", "Repositories"))
	huma.Register(api, huma.Operation{
		OperationID:   "get-repo-browser-asset",
		Method:        http.MethodGet,
		Path:          repoPath + "/browser/asset",
		DefaultStatus: http.StatusOK,
		Summary:       "Get repository browser asset",
		Description:   "Returns raw image bytes for a repository file. Asset reads require ref_type=commit with ref_sha set to a full 40-character commit SHA; branch and tag refs are rejected with mutable_ref_not_allowed.",
		Tags:          []string{"Repositories"},
		Responses:     assetResponses(),
	}, h.getRepoBrowserAsset)
	huma.Register(api, huma.Operation{
		OperationID:   "get-repo-browser-asset-on-host",
		Method:        http.MethodGet,
		Path:          hostRepoPath + "/browser/asset",
		DefaultStatus: http.StatusOK,
		Summary:       "Get repository browser asset",
		Description:   "Returns raw image bytes for a repository file. Asset reads require ref_type=commit with ref_sha set to a full 40-character commit SHA; branch and tag refs are rejected with mutable_ref_not_allowed.",
		Tags:          []string{"Repositories"},
		Responses:     assetResponses(),
	}, h.getRepoBrowserAssetOnHost)
	huma.Get(api, repoPath+"/browser/last-changed", h.getRepoBrowserLastChanged,
		httpapi.DocumentOperation("get-repo-browser-last-changed", "Get repository browser last changed commits", "Repositories"))
	huma.Get(api, hostRepoPath+"/browser/last-changed", h.getRepoBrowserLastChangedOnHost,
		httpapi.DocumentOperation("get-repo-browser-last-changed-on-host", "Get repository browser last changed commits", "Repositories"))
	huma.Get(api, repoPath+"/browser/history", h.getRepoBrowserHistory,
		httpapi.DocumentOperation("get-repo-browser-history", "Get repository browser file history", "Repositories"))
	huma.Get(api, hostRepoPath+"/browser/history", h.getRepoBrowserHistoryOnHost,
		httpapi.DocumentOperation("get-repo-browser-history-on-host", "Get repository browser file history", "Repositories"))
	huma.Get(api, repoPath+"/browser/commit", h.getRepoBrowserCommit,
		httpapi.DocumentOperation("get-repo-browser-commit", "Get repository browser commit", "Repositories"))
	huma.Get(api, hostRepoPath+"/browser/commit", h.getRepoBrowserCommitOnHost,
		httpapi.DocumentOperation("get-repo-browser-commit-on-host", "Get repository browser commit", "Repositories"))
}

func assetResponses() map[string]*huma.Response {
	return map[string]*huma.Response{
		"200": {
			Description: "Image response",
			Content: map[string]*huma.MediaType{
				"image/avif": {Schema: &huma.Schema{Type: "string", Format: "binary"}},
				"image/bmp":  {Schema: &huma.Schema{Type: "string", Format: "binary"}},
				"image/gif":  {Schema: &huma.Schema{Type: "string", Format: "binary"}},
				"image/jpeg": {Schema: &huma.Schema{Type: "string", Format: "binary"}},
				"image/png":  {Schema: &huma.Schema{Type: "string", Format: "binary"}},
				"image/webp": {Schema: &huma.Schema{Type: "string", Format: "binary"}},
			},
		},
	}
}
