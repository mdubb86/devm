// policyd is the TransformService stub for iron-proxy grpc-transform
// contract tests. It implements the allow/deny decision the devm daemon
// will own under the grpc-policy-authority design: hosts on the -allow
// list CONTINUE; everything else is REJECTed with a fully custom
// response (status, X-Devm-Blocked header, JSON body) so tests can pin
// that iron-proxy delivers the custom reject to clients verbatim.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"strings"

	"google.golang.org/grpc"

	transformv1 "devmcontract/ironproxy/transformv1"
)

type server struct {
	transformv1.UnimplementedTransformServiceServer
	allowed map[string]bool
	status  int
}

// hostOnly strips a trailing :port (IPv6 brackets preserved).
func hostOnly(h string) string {
	if i := strings.LastIndex(h, ":"); i > 0 && !strings.Contains(h[i:], "]") {
		return h[:i]
	}
	return h
}

func (s *server) TransformRequest(ctx context.Context, req *transformv1.TransformRequestRequest) (*transformv1.TransformRequestResponse, error) {
	r := req.GetRequest()
	host := hostOnly(r.GetHost())
	if s.allowed[host] {
		return &transformv1.TransformRequestResponse{
			Action: transformv1.TransformAction_TRANSFORM_ACTION_CONTINUE,
		}, nil
	}
	body := fmt.Sprintf(
		`{"blocked_by":"devm-egress-policy","host":%q,"method":%q,"url":%q}`+"\n",
		host, r.GetMethod(), r.GetUrl())
	return &transformv1.TransformRequestResponse{
		Action: transformv1.TransformAction_TRANSFORM_ACTION_REJECT,
		Response: &transformv1.HttpResponse{
			StatusCode: int32(s.status),
			Headers: map[string]*transformv1.HeaderValues{
				"X-Devm-Blocked": {Values: []string{"egress-policy"}},
				"Content-Type":   {Values: []string{"application/json"}},
			},
			Body: []byte(body),
		},
	}, nil
}

func (s *server) TransformResponse(ctx context.Context, req *transformv1.TransformResponseRequest) (*transformv1.TransformResponseResponse, error) {
	return &transformv1.TransformResponseResponse{
		Action: transformv1.TransformAction_TRANSFORM_ACTION_CONTINUE,
	}, nil
}

func main() {
	sock := flag.String("sock", "", "unix socket path to listen on (required)")
	allow := flag.String("allow", "", "comma-separated hosts to allow")
	status := flag.Int("status", 451, "HTTP status for rejects")
	flag.Parse()
	if *sock == "" {
		log.Fatal("-sock is required")
	}

	allowed := map[string]bool{}
	for _, h := range strings.Split(*allow, ",") {
		if h = strings.TrimSpace(h); h != "" {
			allowed[h] = true
		}
	}

	_ = os.Remove(*sock)
	lis, err := net.Listen("unix", *sock)
	if err != nil {
		log.Fatalf("listen: %v", err)
	}
	g := grpc.NewServer()
	transformv1.RegisterTransformServiceServer(g, &server{allowed: allowed, status: *status})
	log.Printf("policyd listening on %s (allow=%v status=%d)", *sock, *allow, *status)
	if err := g.Serve(lis); err != nil {
		log.Fatalf("serve: %v", err)
	}
}
