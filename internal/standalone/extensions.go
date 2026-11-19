package standalone

import "net/http"

func (a *App) registerExtendedDataRoutes(mux *http.ServeMux) {
	mux.Handle("/v1/responses", a.authenticate(http.HandlerFunc(a.handleResponses)))
}
