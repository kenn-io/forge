package server

import (
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"go.kenn.io/forge/internal/server/activityapi"
	"go.kenn.io/forge/internal/server/fleetapi"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/routepolicy"
	"go.kenn.io/forge/internal/server/workspaceapi"
)

func (s *Server) registerAPI(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "get-version",
		Method:      http.MethodGet,
		Path:        "/version",
		Summary:     "Get server version",
		Tags:        []string{"System"},
	}, s.getVersion)
	if s.options.ExecutionWorker {
		s.registerWorkerAPI(api)
		s.workspaceAPI.RegisterExecution(api)
		return
	}

	huma.Get(api, "/activity", s.activityapi.ListActivity,
		httpapi.DocumentOperation("list-activity", "List activity", "Activity"))
	huma.Get(api, "/activity/thread-events", s.activityapi.ListActivityThreadEvents,
		httpapi.DocumentOperation("list-activity-thread-events", "List activity thread events", "Activity"))
	huma.Get(api, "/activity/authors", s.activityapi.ListActivityAuthors,
		httpapi.DocumentOperation("list-activity-authors", "List activity authors", "Activity"))
	s.kataAPI.Register(api)
	s.docsAPI.Register(api)
	s.archiveapi.RegisterArchiveAPI(api)
	huma.Register(api, huma.Operation{
		OperationID:   "list-notifications",
		Method:        http.MethodGet,
		Path:          "/notifications",
		DefaultStatus: http.StatusOK,
		Summary:       "List notifications",
		Tags:          []string{"Activity"},
	}, s.notificationapi.ListNotifications)
	huma.Register(api, huma.Operation{
		OperationID:   "sync-notifications",
		Method:        http.MethodPost,
		Path:          "/notifications/sync",
		DefaultStatus: http.StatusAccepted,
		Summary:       "Sync notifications",
		Tags:          []string{"Sync"},
	}, s.notificationapi.SyncNotifications)
	huma.Register(api, huma.Operation{
		OperationID:   "mark-notifications-read",
		Method:        http.MethodPost,
		Path:          "/notifications/read",
		DefaultStatus: http.StatusOK,
		Summary:       "Mark notifications read",
		Tags:          []string{"Activity"},
	}, s.notificationapi.MarkNotificationsRead)
	huma.Register(api, huma.Operation{
		OperationID:   "mark-notifications-done",
		Method:        http.MethodPost,
		Path:          "/notifications/done",
		DefaultStatus: http.StatusOK,
		Summary:       "Mark notifications done",
		Tags:          []string{"Activity"},
	}, s.notificationapi.MarkNotificationsDone)
	huma.Register(api, huma.Operation{
		OperationID:   "mark-notifications-undone",
		Method:        http.MethodPost,
		Path:          "/notifications/undone",
		DefaultStatus: http.StatusOK,
		Summary:       "Mark notifications undone",
		Tags:          []string{"Activity"},
	}, s.notificationapi.MarkNotificationsUndone)
	s.pullAPI.Register(api)
	s.issueAPI.Register(api)
	s.providerapi.RegisterProviderRepoAPI(api)
	s.workflowAPI.Register(api)
	s.repoBrowserAPI.Register(api)
	s.fleetAPI.Register(api)
	s.registerSpokePreparationAPI(api)

	huma.Register(api, huma.Operation{
		OperationID:   "list-repo-summaries",
		Method:        http.MethodGet,
		Path:          "/repos/summary",
		DefaultStatus: http.StatusOK,
		Summary:       "List repository summaries",
		Tags:          []string{"Repositories"},
	}, s.activityapi.ListRepoSummaries)
	huma.Register(api, huma.Operation{
		OperationID:   "set-starred",
		Method:        http.MethodPut,
		Path:          "/starred",
		DefaultStatus: http.StatusOK,
		Summary:       "Star repository",
		Tags:          []string{"Settings"},
	}, s.activityapi.SetStarred)
	huma.Register(api, huma.Operation{
		OperationID:   "unset-starred",
		Method:        http.MethodDelete,
		Path:          "/starred",
		DefaultStatus: http.StatusOK,
		Summary:       "Unstar repository",
		Tags:          []string{"Settings"},
	}, s.activityapi.UnsetStarred)

	huma.Get(api, "/repos", s.activityapi.ListRepos,
		httpapi.DocumentOperation("list-repos", "List repositories", "Repositories"))
	huma.Register(api, huma.Operation{
		OperationID:   "preview-repos",
		Method:        http.MethodPost,
		Path:          "/repos/preview",
		DefaultStatus: http.StatusOK,
		Summary:       "Preview repositories",
		Tags:          []string{"Repositories"},
	}, s.repoapi.PreviewRepos)
	huma.Register(api, huma.Operation{
		OperationID:   "bulk-add-repos",
		Method:        http.MethodPost,
		Path:          "/repos/bulk",
		DefaultStatus: http.StatusCreated,
		Summary:       "Bulk add repositories",
		Tags:          []string{"Repositories"},
	}, s.bulkAddRepos)
	s.registerSettingsAPI(api)
	s.registerProviderFederationAPI(api)
	s.registerFederationEventAPI(api)
	s.browserloginapi.RegisterBrowserLoginAPI(api)
	huma.Register(api, huma.Operation{
		OperationID:   "trigger-sync",
		Method:        http.MethodPost,
		Path:          "/sync",
		DefaultStatus: http.StatusAccepted,
		Summary:       "Trigger sync",
		Tags:          []string{"Sync"},
	}, s.activityapi.TriggerSync)
	huma.Register(api, huma.Operation{
		OperationID: "stream-events",
		Method:      http.MethodGet,
		Path:        "/events",
		Summary:     "Stream server events",
		Tags:        []string{"System"},
		Responses: map[string]*huma.Response{
			"200": {
				Description: "Server-sent event stream",
				Content: map[string]*huma.MediaType{
					"text/event-stream": {},
				},
			},
		},
	}, s.streamapi.StreamEvents)
	huma.Get(api, "/sync/status", s.activityapi.SyncStatus,
		httpapi.DocumentOperation("get-sync-status", "Get sync status", "Sync"))
	huma.Get(api, "/rate-limits", s.notificationapi.GetRateLimits,
		httpapi.DocumentOperation("get-rate-limits", "Get rate limits", "Sync"))
	huma.Register(api, huma.Operation{
		OperationID:   "capture-telemetry-event",
		Method:        http.MethodPost,
		Path:          "/telemetry/events",
		DefaultStatus: http.StatusAccepted,
		Summary:       "Capture telemetry event",
		Tags:          []string{"System"},
	}, s.telemetryapi.CaptureTelemetryEvent)
	huma.Register(api, huma.Operation{
		OperationID: "get-roborev-status",
		Method:      http.MethodGet,
		Path:        "/roborev/status",
		Summary:     "Get roborev status",
		Tags:        []string{"Roborev"},
	}, s.roborevapi.GetRoborevStatus)
	huma.Register(api, huma.Operation{
		OperationID:   "list-roborev-configured-repositories",
		Method:        http.MethodGet,
		Path:          "/roborev/configured-repositories",
		DefaultStatus: http.StatusOK,
		Summary:       "List repositories configured for Roborev",
		Tags:          []string{"Roborev"},
	}, s.roborevapi.ListRoborevConfiguredRepositories)

	s.workspaceAPI.Register(api)

	huma.Register(api, huma.Operation{
		OperationID: "complete-filesystem-path",
		Method:      http.MethodGet,
		Path:        "/filesystem/complete",
		Summary:     "Complete a local filesystem path",
		Tags:        []string{"System"},
	}, s.routepolicy.CompleteFilesystemPath)
	huma.Register(api, huma.Operation{
		OperationID: "validate-filesystem-repo",
		Method:      http.MethodGet,
		Path:        "/filesystem/validate-repo",
		Summary:     "Resolve a path to a repository root",
		Tags:        []string{"System"},
	}, s.routepolicy.ValidateFilesystemRepo)
	huma.Register(api, huma.Operation{
		OperationID: "list-user-repositories",
		Method:      http.MethodGet,
		Path:        "/platform/user-repositories",
		Summary:     "List the authenticated platform CLI user's repositories",
		Tags:        []string{"System"},
	}, s.repoapi.ListUserRepositories)
	huma.Register(api, huma.Operation{
		OperationID: "get-tooling-status",
		Method:      http.MethodGet,
		Path:        "/tooling-status",
		Summary:     "Report git/gh/glab CLI availability and auth",
		Tags:        []string{"System"},
	}, s.repoapi.GetToolingStatus)
	huma.Register(api, huma.Operation{
		OperationID: "launch-host-runtime-session",
		Method:      http.MethodPost,
		Path:        "/runtime/sessions",
		Summary:     "Launch host runtime session",
		Tags:        []string{"Runtime"},
	}, s.hostapi.LaunchHostRuntimeSession)
	huma.Register(api, huma.Operation{
		OperationID: "list-host-runtime-sessions",
		Method:      http.MethodGet,
		Path:        "/runtime/sessions",
		Summary:     "List host runtime sessions",
		Tags:        []string{"Runtime"},
	}, s.hostapi.ListHostRuntimeSessions)
	huma.Register(api, huma.Operation{
		OperationID:   "stop-host-runtime-session",
		Method:        http.MethodDelete,
		Path:          "/runtime/sessions/{session_key}",
		DefaultStatus: http.StatusNoContent,
		Summary:       "Stop host runtime session",
		Tags:          []string{"Runtime"},
	}, s.hostapi.StopHostRuntimeSession)
	huma.Register(api, huma.Operation{
		OperationID: "get-host-runtime-session-attach-spec",
		Method:      http.MethodGet,
		Path:        "/runtime/sessions/{session_key}/attach-spec",
		Summary:     "Get host runtime session attach spec",
		Tags:        []string{"Runtime"},
	}, s.hostapi.GetHostRuntimeSessionAttachSpec)
	s.registerDevboxAPI(api)
}

func NewOpenAPI() *huma.OpenAPI {
	mux := http.NewServeMux()
	s := wiredServer(&Server{})
	api := humago.NewWithPrefix(mux, "/api/v1", activityapi.ApiConfig("/"))
	s.registerAPI(api)
	s.registerWorkerAPI(api)
	return api.OpenAPI()
}

// NewClientOpenAPI includes hidden operations used by first-party clients.
func NewClientOpenAPI() *huma.OpenAPI {
	config := activityapi.ApiConfig("/")
	config.OpenAPI = NewOpenAPI()
	api := humago.New(http.NewServeMux(), config)
	(*workspaceapi.Handler)(nil).RegisterTerminalClipboard(api, false)
	(*fleetapi.Handler)(nil).RegisterWorkspaceCleanup(api, false)
	return api.OpenAPI()
}

// NewHealthOpenAPI describes the health API served outside /api/v1.
func NewHealthOpenAPI() *huma.OpenAPI {
	api := humago.New(http.NewServeMux(), routepolicy.HealthAPIConfig())
	(wiredServer(&Server{})).routepolicy.RegisterHealthAPI(api)
	return api.OpenAPI()
}

// --- Commits ---
