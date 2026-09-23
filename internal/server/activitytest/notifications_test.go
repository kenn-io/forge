package activitytest

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/server"
	serverfake "go.kenn.io/forge/internal/testutil/serverfake"
)

func TestNotificationsAPIRejectsNilConfigAccess(t *testing.T) {
	require := require.New(t)
	database := serverfake.OpenTestDB(t)
	id := serverfake.SeedServerNotification(t, database)
	s := server.New(database, nil, nil, "/", nil, server.ServerOptions{})
	ts := httptest.NewServer(s)
	defer ts.Close()

	body, err := json.Marshal(map[string]any{"ids": []int64{id}})
	require.NoError(err)
	respReq, err := http.NewRequestWithContext(t.Context(), http.MethodPost, ts.URL+"/api/v1/notifications/read", bytes.NewReader(body))
	require.NoError(err)
	respReq.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(respReq)
	require.NoError(err)
	defer resp.Body.Close()
	require.Equal(http.StatusForbidden, resp.StatusCode)
}
