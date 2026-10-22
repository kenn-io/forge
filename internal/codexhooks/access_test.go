package codexhooks

import (
	"bytes"
	"encoding/json/jsontext"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadAccess(t *testing.T) {
	for _, tc := range []struct {
		name, reply, sandbox, approval, failure, wantRequest string
		config                                               map[string]any
	}{
		{name: "full access", reply: `{"result":{"sandbox":{"type":"dangerFullAccess"},"approvalPolicy":"never"}}`, sandbox: "dangerFullAccess", approval: "never", wantRequest: `{"id":1,"method":"thread/start","params":{"ephemeral":true,"config":{"projects":{"/workspace":{"trust_level":"trusted"}}}}}`},
		{name: "caller config preserved with projects overridden", reply: `{"result":{"sandbox":{"type":"readOnly"},"approvalPolicy":"on-request"}}`, sandbox: "readOnly", approval: "on-request", config: map[string]any{"model": "example-model", "projects": map[string]any{"/workspace": map[string]any{"trust_level": "untrusted"}}}, wantRequest: `{"id":1,"method":"thread/start","params":{"ephemeral":true,"config":{"model":"example-model","projects":{"/workspace":{"trust_level":"trusted"}}}}}`},
		{name: "granular approval", reply: `{"result":{"sandbox":{"type":"readOnly"},"approvalPolicy":{"granular":{"sandbox_approval":true}}}}`, sandbox: "readOnly"},
		{name: "rpc error", reply: `{"error":{"message":"policy rejected"}}`, failure: "policy rejected"},
		{name: "missing sandbox type", reply: `{"result":{"sandbox":{"type":""},"approvalPolicy":"never"}}`, failure: "no sandbox type"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var sent bytes.Buffer
			c := &client{input: &sent, output: jsontext.NewDecoder(strings.NewReader(`{"id":1,` + tc.reply[1:]))}
			sandbox, approval, err := c.readAccess("/workspace", tc.config)
			if tc.failure != "" {
				require.ErrorContains(t, err, tc.failure)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tc.sandbox, sandbox)
			assert.Equal(t, tc.approval, approval)
			if tc.wantRequest != "" {
				assert.JSONEq(t, tc.wantRequest, sent.String())
			}
		})
	}
}
