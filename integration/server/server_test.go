package main

import (
	"bytes"
	"context"
	"fmt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"io/ioutil"
	"net"
	"net/http"
	"os"
	"os/exec"
	"testing"
	"time"
)

type portAssignments struct {
	gofailPort int
	serverPort int
}

type testRequest interface {
	AssertResponse(t *testing.T)
	SetupPortAssignments(ports portAssignments)
}

type response struct {
	statusCode int
	body       string
}

type request struct {
	port       int
	methodType string
	endpoint   string
	args       []string
	expected   response
}

type gofailTestRequest struct {
	request

	// requestTypes: put, failpoints, listall, list, count, deactivate
	requestType string
}

func (g *gofailTestRequest) AssertResponse(t *testing.T) {
	require.NotEqual(t, g.port, 0, "port is not set")

	endpoint := g.endpoint
	payload := ""
	var methodType string
	switch g.requestType {
	case "put":
		methodType = http.MethodPut
		break
	case "failpoints":
		methodType = http.MethodPut
		assert.Equalf(t, g.endpoint, "", "endpoint should be empty for `failpoints' request type")
		assert.Nil(t, g.args, "args should be nil for `failpoints' request type")
		endpoint = "failpoints"
		break
	case "listall":
		methodType = http.MethodGet
		assert.Equalf(t, g.endpoint, "", "endpoint should be empty for `listall' request type")
		assert.Nil(t, g.args, "args should be nil for `listall' request type")
		endpoint = ""
		break
	case "list":
		methodType = http.MethodGet
		assert.Nil(t, g.args, "args should be nil for `listall' request type")
		break
	case "count":
		methodType = http.MethodGet
		//TODO implement me
		panic("implement me")
	case "deactivate":
		methodType = http.MethodDelete
		//TODO implement me
		panic("implement me")
	default:
		t.Errorf("unknown request type: %s", g.requestType)
		return
	}

	body, statusCode, err := sendRequest(g.port, methodType, endpoint, []byte(payload))
	assert.NoError(t, err)
	assert.Equal(t, g.expected.statusCode, statusCode)
	assert.Equal(t, g.expected.body, body)
}

func (g *gofailTestRequest) SetupPortAssignments(ports portAssignments) {
	g.port = ports.gofailPort
}

func sendRequest(port int, method string, endpoint string, data []byte) (string, int, error) {
	url := fmt.Sprintf("http://localhost:%d/%s", port, endpoint)
	req, err := http.NewRequest(method, url, bytes.NewBuffer(data))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", 0, err
	}

	return string(body), resp.StatusCode, nil
}

func getOpenPorts(count int) ([]int, error) {
	ports := make([]int, 0, count)
	for i := 0; i < count; i++ {
		listener, err := net.Listen("tcp", "localhost:0")
		if err != nil {
			return nil, err
		}
		defer listener.Close()

		addr := listener.Addr().(*net.TCPAddr)
		ports = append(ports, addr.Port)
	}
	return ports, nil
}

func TestAll(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ports, err := getOpenPorts(2)
	require.NoError(t, err)
	pas := portAssignments{gofailPort: ports[0], serverPort: ports[1]}

	waitForServer := make(chan any)

	// Spawn server.go in a goroutine
	go func() {
		cmd := exec.CommandContext(ctx, "go", "run", "main.go", fmt.Sprintf("%d", pas.serverPort))
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Env = append(os.Environ(), fmt.Sprintf("GOFAIL_HTTP=%d", pas.gofailPort))
		t.Logf("Starting server: GOFAIL_HTTP=%d %v", pas.gofailPort, cmd)

		err := cmd.Start()
		waitForServer <- struct{}{}

		if err != nil {
			t.Errorf("Failed to run server: %v", err)
			return
		}

		t.Logf("Waiting for server to exit, pid %d", cmd.Process.Pid)
		err = cmd.Wait()
		t.Logf("Server exited: %v", err)
		assert.NoError(t, err)
	}()

	<-waitForServer

	tests := []struct {
		name     string
		requests []testRequest
	}{
		// TODO: test cases
		{
			name: "BasicGet",
			requests: []testRequest{
				&gofailTestRequest{
					requestType: "listall",
					request: request{
						endpoint: "",
						args:     nil,
						expected: response{
							statusCode: 200,
							body:       "Hello, World!",
						},
					},
				},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for _, req := range test.requests {
				req.SetupPortAssignments(pas)
				req.AssertResponse(t)
			}
		})
	}
}
