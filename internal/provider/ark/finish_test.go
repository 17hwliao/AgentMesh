package ark

import (
	"agentmesh/internal/provider"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestContentFilteredFinishIsNotReportedAsNormalStop(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"content_filter\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer s.Close()
	a := testAdapter(t, s.URL)
	finish := ""
	if e := a.Stream(context.Background(), testRequest(), func(c provider.Chunk) error { finish = c.FinishReason; return nil }); e != nil || finish != "content_filter" {
		t.Fatal("filter information lost", finish, e)
	}
}
