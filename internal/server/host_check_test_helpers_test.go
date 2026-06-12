package server

import "net/http"

func setAcceptedHostForServerTest(req *http.Request, srv *Server) {
	req.Host = srv.hostOpts.Bind.String()
}
