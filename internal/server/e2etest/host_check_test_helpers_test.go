package e2etest

import "net/http"

func setAcceptedHostForE2ETest(req *http.Request) {
	req.Host = "127.0.0.1:8091"
}
