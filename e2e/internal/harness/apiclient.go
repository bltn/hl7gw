//go:build e2e

package harness

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/legacy"
)

type APIResponse struct {
	Status      int
	Body        []byte
	ReqSpecErr  error
	RespSpecErr error
}

type RawAPIResponse struct {
	Status int
	Header http.Header
	Body   []byte
}

type Operation struct {
	Method string
	Path   string
}

type loadedSpec struct {
	router routers.Router
	doc    *openapi3.T
}

var spec = sync.OnceValues(loadSpec)

func loadSpec() (loadedSpec, error) {
	dir, err := moduleDir()
	if err != nil {
		return loadedSpec{}, err
	}

	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromFile(filepath.Join(dir, "openapi.yaml"))
	if err != nil {
		return loadedSpec{}, fmt.Errorf("load openapi.yaml: %w", err)
	}
	if err := doc.Validate(loader.Context); err != nil {
		return loadedSpec{}, fmt.Errorf("validate openapi.yaml: %w", err)
	}
	doc.Servers = nil

	router, err := legacy.NewRouter(doc)
	if err != nil {
		return loadedSpec{}, fmt.Errorf("build spec router: %w", err)
	}
	return loadedSpec{router: router, doc: doc}, nil
}

func SpecOperations() ([]Operation, error) {
	s, err := spec()
	if err != nil {
		return nil, err
	}

	var ops []Operation
	for path, item := range s.doc.Paths.Map() {
		for method := range item.Operations() {
			ops = append(ops, Operation{Method: method, Path: path})
		}
	}
	return ops, nil
}

func (gw *Gateway) APIGet(pathAndQuery, authorization string) (APIResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := gw.apiRequest(ctx, http.MethodGet, pathAndQuery, authorization, nil)
	if err != nil {
		return APIResponse{}, err
	}
	return apiDo(ctx, req, pathAndQuery)
}

func (gw *Gateway) APIPost(pathAndQuery, authorization string, body []byte) (APIResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := gw.apiRequest(ctx, http.MethodPost, pathAndQuery, authorization, body)
	if err != nil {
		return APIResponse{}, err
	}
	return apiDo(ctx, req, pathAndQuery)
}

func (gw *Gateway) WaitForMetricLine(line string, timeout time.Duration) (string, bool) {
	var body string
	found := eventually(timeout, func() bool {
		resp, err := gw.APIRaw(http.MethodGet, "/metrics", "", nil)
		if err != nil {
			return false
		}
		body = string(resp.Body)
		return strings.Contains(body, line)
	})
	return body, found
}

func (gw *Gateway) APIRaw(method, pathAndQuery, authorization string, body []byte) (RawAPIResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := gw.apiRequest(ctx, method, pathAndQuery, authorization, body)
	if err != nil {
		return RawAPIResponse{}, err
	}
	httpResp, err := http.DefaultClient.Do(req)
	if err != nil {
		return RawAPIResponse{}, err
	}
	defer func() { _ = httpResp.Body.Close() }()
	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return RawAPIResponse{}, err
	}
	return RawAPIResponse{Status: httpResp.StatusCode, Header: httpResp.Header, Body: respBody}, nil
}

func (gw *Gateway) apiRequest(ctx context.Context, method, pathAndQuery, authorization string, body []byte) (*http.Request, error) {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, gw.APIURL()+pathAndQuery, r)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	return req, nil
}

func apiDo(ctx context.Context, req *http.Request, pathAndQuery string) (APIResponse, error) {
	s, err := spec()
	if err != nil {
		return APIResponse{}, err
	}

	route, pathParams, err := s.router.FindRoute(req)
	if err != nil {
		return APIResponse{}, fmt.Errorf("%s is not a route in openapi.yaml: %w", pathAndQuery, err)
	}
	reqInput := &openapi3filter.RequestValidationInput{
		Request:    req,
		PathParams: pathParams,
		Route:      route,
		Options:    &openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc},
	}
	reqSpecErr := openapi3filter.ValidateRequest(ctx, reqInput)

	httpResp, err := http.DefaultClient.Do(req)
	if err != nil {
		return APIResponse{}, err
	}
	defer func() { _ = httpResp.Body.Close() }()
	body, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return APIResponse{}, err
	}

	respSpecErr := openapi3filter.ValidateResponse(ctx, &openapi3filter.ResponseValidationInput{
		RequestValidationInput: reqInput,
		Status:                 httpResp.StatusCode,
		Header:                 httpResp.Header,
		Body:                   io.NopCloser(bytes.NewReader(body)),
		Options:                reqInput.Options,
	})

	return APIResponse{
		Status:      httpResp.StatusCode,
		Body:        body,
		ReqSpecErr:  reqSpecErr,
		RespSpecErr: respSpecErr,
	}, nil
}
