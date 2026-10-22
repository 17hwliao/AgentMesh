package modelrpc

import (
	"agentmesh/internal/auth"
	"agentmesh/internal/provider"
	"agentmesh/internal/securegrpc"
	"agentmesh/internal/testcert"
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/png"
	"math/rand"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

type visionFixture struct {
	messages  chan []provider.Message
	cancelled chan struct{}
}

func (visionFixture) Name() string                     { return "compatible" }
func (visionFixture) Health(context.Context) error     { return nil }
func (visionFixture) BillingModel(string) string       { return "actual-vision" }
func (visionFixture) SupportsImages(alias string) bool { return alias == "v" }
func (p visionFixture) Stream(ctx context.Context, r provider.ChatRequest, emit provider.Emit) error {
	p.messages <- r.Messages
	if r.Messages[len(r.Messages)-1].Content == "cancel" {
		<-ctx.Done()
		p.cancelled <- struct{}{}
		return ctx.Err()
	}
	if e := emit(provider.Chunk{Delta: "image answer"}); e != nil {
		return e
	}
	return emit(provider.Chunk{Usage: &provider.Usage{InputTokens: 300, OutputTokens: 2}})
}
func TestMTLSLargeImageHistoryAndCancellation(t *testing.T) {
	ca, sc, sk, cc, ck, e := testcert.Generate(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	token := strings.Repeat("v", 32)
	limit := provider.MaxChatBytes + (64 << 10)
	server, e := securegrpc.Server(securegrpc.Config{CA: ca, Cert: sc, Key: sk, Token: token, MaxMessageBytes: limit})
	if e != nil {
		t.Fatal(e)
	}
	defer server.Stop()
	f := visionFixture{make(chan []provider.Message, 2), make(chan struct{}, 1)}
	RegisterModelRunnerServer(server, NewServer([]provider.Provider{f}, func(_ context.Context, tenant, model, name string) bool {
		return tenant == "viewer" && model == "v" && name == "compatible"
	}))
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	go server.Serve(listener)
	conn, e := securegrpc.Dial(listener.Addr().String(), securegrpc.Config{CA: ca, Cert: cc, Key: ck, ServerName: "localhost", Token: token, MaxMessageBytes: limit})
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	a := NewAdapter(conn, "compatible", map[string]string{"v": "actual-vision"}, "")
	a.VisionModels = []string{"v"}
	img := image.NewRGBA(image.Rect(0, 0, 220, 220))
	rng := rand.New(rand.NewSource(42))
	rng.Read(img.Pix)
	for i := 3; i < len(img.Pix); i += 4 {
		img.Pix[i] = 255
	}
	var encoded bytes.Buffer
	if png.Encode(&encoded, img) != nil {
		t.Fatal("image encode")
	}
	uri := "data:image/png;base64," + base64.StdEncoding.EncodeToString(encoded.Bytes())
	if len(uri) <= 128<<10 {
		t.Fatal("fixture did not exercise old transport bound")
	}
	msgs := []provider.Message{{Role: "system", Content: "system"}, {Role: "user", Parts: []provider.ContentPart{{Type: "image_url", ImageURL: &provider.ImageURL{URL: uri}}}}, {Role: "assistant", Content: "history"}, {Role: "user", Content: "followup"}}
	key := "private-mtls-image-fixture-key"
	store, e := auth.Bootstrap(func(k string) string {
		return map[string]string{"AGENTMESH_BOOTSTRAP_API_KEY": key, "AGENTMESH_BOOTSTRAP_TENANT_ID": "viewer", "AGENTMESH_BOOTSTRAP_MODEL_ROUTES": `{"v":["compatible"]}`}[k]
	})
	if e != nil {
		t.Fatal(e)
	}
	invoke := func(ctx context.Context) error {
		var err error
		h := auth.Authenticate(store, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			err = a.Stream(r.Context(), provider.ChatRequest{Model: "v", MaxTokens: 32, Messages: msgs}, func(provider.Chunk) error { return nil })
		}))
		r := httptest.NewRequest("POST", "/", nil).WithContext(ctx)
		r.Header.Set("Authorization", "Bearer "+key)
		h.ServeHTTP(httptest.NewRecorder(), r)
		return err
	}
	if e := invoke(context.Background()); e != nil {
		t.Fatal(e)
	}
	if got := <-f.messages; !reflect.DeepEqual(got, msgs) {
		t.Fatal("large image/context changed across mTLS")
	}
	msgs[3].Content = "cancel"
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if invoke(ctx) == nil {
		t.Fatal("cancel accepted")
	}
	select {
	case <-f.cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("image request cancellation did not propagate")
	}
}
