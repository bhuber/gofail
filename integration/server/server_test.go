package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"
)

// portAssignments is a struct that holds the ports for the gofail and server servers
type portAssignments struct {
	// gofailPort is the port the gofail failpoint control API is running on
	gofailPort int

	// serverPort is the port the server test status API is running on
	serverPort int
}

type testRequest interface {
	// AssertResponse is the heart of the test logic. It sends the request to the server,
	// then checks the response matches expectations.
	AssertResponse(t *testing.T)
	// SetupPortAssignments is a helper for setting correct request.port value
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

// serverTestRequest is a test request for the server test status API
type serverTestRequest struct {
	request
}

func (s *serverTestRequest) AssertResponse(t *testing.T) {
	endpoint := "call/" + s.endpoint
	if len(s.args) > 0 {
		// http server interprets "?args=" as having a single argument of value ""
		endpoint += "?args=" + strings.Join(s.args, ",")
	}

	body, statusCode, err := sendRequest(t, s.port, http.MethodGet, endpoint, []byte{})
	assert.NoError(t, err)
	assert.Equal(t, s.expected.statusCode, statusCode)
	assert.Equal(t, s.expected.body, body)
}

func (s *serverTestRequest) SetupPortAssignments(ports portAssignments) {
	s.port = ports.serverPort
}

// gofailTestRequest is a test request for the gofail server control API
type gofailTestRequest struct {
	request

	// requestType is the kind of request we're making to the failpoint control API.
	// These correspond to the operations described in the "HTTP endpoint" of the README.
	//
	// Valid values are put, failpoints, listall, list, count, deactivate
	// * put: sets the value of a failpoint, enabling it
	// * failpoints: sets the values of multiple failpoints at once
	// * listall: lists all failpoints and their values
	// * list: lists the value of a single failpoint
	// * count: gets the number of times a failpoint has been hit
	// * deactivate: clears a given failpoint
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
		assert.NotEmpty(t, g.endpoint, "endpoint should not be empty for `put' request type")
		assert.Equalf(t, len(g.args), 1, "args should have exactly one element for `put' request type")
		payload = g.args[0]
		break
	case "failpoints":
		methodType = http.MethodPut
		assert.Equalf(t, g.endpoint, "", "endpoint should be empty for `failpoints' request type")
		endpoint = "failpoints"
		payload = strings.Join(g.args, ";")
		break
	case "listall":
		methodType = http.MethodGet
		assert.Equalf(t, g.endpoint, "", "endpoint should be empty for `listall' request type")
		assert.Nil(t, g.args, "args should be nil for `listall' request type")
		endpoint = ""
		break
	case "list":
		methodType = http.MethodGet
		assert.NotEmpty(t, g.endpoint, "endpoint should not be empty for `list' request type")
		assert.Nil(t, g.args, "args should be nil for `list' request type")
		break
	case "count":
		methodType = http.MethodGet
		assert.NotEmpty(t, g.endpoint, "endpoint should not be empty for `count' request type")
		assert.Nil(t, g.args, "args should be nil for `count' request type")
		endpoint += "/count"
	case "deactivate":
		methodType = http.MethodDelete
		assert.NotEmpty(t, g.endpoint, "endpoint should not be empty for `delete' request type")
		assert.Nil(t, g.args, "args should be nil for `deactivate' request type")
	default:
		t.Errorf("unknown request type: %s", g.requestType)
		return
	}

	body, statusCode, err := sendRequest(t, g.port, methodType, endpoint, []byte(payload))
	assert.NoError(t, err)
	assert.Equal(t, g.expected.statusCode, statusCode)
	if g.requestType != "listall" {
		assert.Equal(t, g.expected.body, body)
	} else {
		// listall responses don't guarantee an order, so we need a more sophisticated test.

		// This is a pretty naive way to convert strings to maps, and will break
		// if any of the keys/values contain the separator characters.
		stringToMap := func(s, entrySep, kvSep string) map[string]string {
			result := make(map[string]string)
			entries := strings.Split(s, entrySep)
			for _, entry := range entries {
				kv := strings.SplitN(entry, kvSep, 2)
				if len(kv) == 2 {
					result[kv[0]] = kv[1]
				}
			}
			return result
		}

		expected := stringToMap(g.expected.body, "\n", "=")
		actual := stringToMap(body, "\n", "=")
		if !reflect.DeepEqual(expected, actual) {
			t.Errorf("listall response did not match:\n\tExpected:\t%#v\n\tActual:\t\t%#v", expected, actual)
		}
	}
}

func (g *gofailTestRequest) SetupPortAssignments(ports portAssignments) {
	g.port = ports.gofailPort
}

func sendRequest(t *testing.T, port int, method string, endpoint string, data []byte) (string, int, error) {
	url := fmt.Sprintf("http://localhost:%d/%s", port, endpoint)
	req, err := http.NewRequest(method, url, bytes.NewBuffer(data))
	if err != nil {
		return "", 0, err
	}
	t.Logf("Sending request: %s %s %s", method, url, data)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer assert.NoError(t, req.Body.Close())

	body, err := io.ReadAll(resp.Body)
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
	timeout := 10 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	ports, err := getOpenPorts(2)
	require.NoError(t, err)
	pas := portAssignments{gofailPort: ports[0], serverPort: ports[1]}

	waitForServer := make(chan any)

	// Spawn server.go in a goroutine
	go func() {
		defer func() {
			println("Server goroutine exited")
		}()

		pipeToStdout := func(reader io.ReadCloser) {
			scanner := bufio.NewScanner(reader)
			for scanner.Scan() {
				fmt.Println("[Test Server] " + scanner.Text())
			}
		}

		cmd := exec.CommandContext(ctx, "go", "run", "main.go", fmt.Sprintf("%d", pas.serverPort))

		stdoutReader, err := cmd.StdoutPipe()
		stderrReader, err := cmd.StderrPipe()
		go pipeToStdout(stdoutReader)
		go pipeToStdout(stderrReader)

		cmd.WaitDelay = timeout
		cmd.Env = append(os.Environ(), fmt.Sprintf("GOFAIL_HTTP=:%d", pas.gofailPort))
		t.Logf("Starting server: GOFAIL_HTTP=:%d %v", pas.gofailPort, cmd)

		err = cmd.Start()

		t.Logf("Waiting for server to be available, pid %d", cmd.Process.Pid)
		assert.Eventuallyf(t, func() bool {
			_, statusCode, _ := sendRequest(t, pas.serverPort, http.MethodGet, "call/ExampleFunc", []byte{})
			return statusCode == 200
		}, timeout, 100*time.Millisecond, "Server did not become available in time")
		waitForServer <- struct{}{}

		if err != nil {
			t.Errorf("Failed to run server: %v", err)
			return
		}

		t.Logf("Waiting for server to exit, pid %d", cmd.Process.Pid)
		err = cmd.Wait()

		// We always stop the server by cancelling the context, so this should always be sigkill
		t.Logf("Server exited (sigkill is expected) with error: %v, context cancellation reason %v", err, ctx.Err())
		assert.Error(t, err)

		waitForServer <- struct{}{}
	}()

	defer func() {
		time.Sleep(1 * time.Second)
		cancel()
		<-waitForServer
	}()
	<-waitForServer

	// Keep in mind server state is preserved between test cases, which means
	// earlier tests may affect later ones.  You can mostly reset state by sending
	// a failpoints request with empty expressions.
	tests := []struct {
		name     string
		requests []testRequest
	}{
		{
			name: "Empty listall",
			requests: []testRequest{
				&gofailTestRequest{
					requestType: "listall",
					request: request{
						expected: response{
							statusCode: 200,
							body:       "ExampleLabels=\nExampleOneLine=\nExampleString=\n",
						},
					},
				},
			},
		},
		{
			name: "list for disabled failpoint returns 404",
			requests: []testRequest{
				&gofailTestRequest{
					requestType: "list",
					request: request{
						endpoint: "ExampleString",
						expected: response{
							statusCode: 404,
							body:       "failed to GET: failpoint: failpoint is disabled\n\n",
						},
					},
				},
			},
		},
		{
			name: "list for invalid failpoint returns 404",
			requests: []testRequest{
				&gofailTestRequest{
					requestType: "list",
					request: request{
						endpoint: "InvalidFailpoint",
						expected: response{
							statusCode: 404,
							body:       "failed to GET: failpoint: failpoint does not exist\n\n",
						},
					},
				},
			},
		},
		{
			name: "count fails for disabled failpoints",
			requests: []testRequest{
				&gofailTestRequest{
					requestType: "count",
					request: request{
						endpoint: "ExampleLabels",
						expected: response{
							statusCode: 500,
							body:       "failed to GET: failpoint: failpoint is disabled\n",
						},
					},
				},
			},
		},
		{
			name: "count fails for invalid failpoints",
			requests: []testRequest{
				&gofailTestRequest{
					requestType: "count",
					request: request{
						endpoint: "InvalidFailpoint",
						expected: response{
							statusCode: 404,
							body:       "failed to GET: failpoint: failpoint does not exist\n",
						},
					},
				},
			},
		},
		{
			name: "count starts at 0",
			requests: []testRequest{
				&gofailTestRequest{
					requestType: "put",
					request: request{
						endpoint: "ExampleString",
						args:     []string{"return(\"fail string\")"},
						expected: response{
							statusCode: 204,
							body:       "",
						},
					},
				},
				&gofailTestRequest{
					requestType: "count",
					request: request{
						endpoint: "ExampleString",
						expected: response{
							statusCode: 200,
							body:       "0",
						},
					},
				},
			},
		},
		{
			name: "list works for enabled failpoints",
			requests: []testRequest{
				&gofailTestRequest{
					requestType: "list",
					request: request{
						endpoint: "ExampleString",
						expected: response{
							statusCode: 200,
							body:       "return(\"fail string\")\n",
						},
					},
				},
			},
		},
		{
			name: "listall after put",
			requests: []testRequest{
				&gofailTestRequest{
					requestType: "listall",
					request: request{
						expected: response{
							statusCode: 200,
							body:       "ExampleLabels=\nExampleOneLine=\nExampleString=return(\"fail string\")\n",
						},
					},
				},
			},
		},
		{
			name: "count increments to 1",
			requests: []testRequest{
				&serverTestRequest{
					request: request{
						endpoint: "ExampleFunc",
						expected: response{
							statusCode: 200,
							body:       "\"fail string\"",
						},
					},
				},
				&gofailTestRequest{
					requestType: "count",
					request: request{
						endpoint: "ExampleString",
						expected: response{
							statusCode: 200,
							body:       "1",
						},
					},
				},
			},
		},
		{
			name: "putting a new value updates an existing failpoint",
			requests: []testRequest{
				&gofailTestRequest{
					requestType: "put",
					request: request{
						endpoint: "ExampleString",
						args:     []string{"return(\"new fail string\")"},
						expected: response{
							statusCode: 204,
							body:       "",
						},
					},
				},
				&gofailTestRequest{
					requestType: "count",
					request: request{
						endpoint: "ExampleString",
						expected: response{
							statusCode: 200,
							body:       "0",
						},
					},
				},
				&serverTestRequest{
					request: request{
						endpoint: "ExampleFunc",
						expected: response{
							statusCode: 200,
							body:       "\"new fail string\"",
						},
					},
				},
				&gofailTestRequest{
					requestType: "count",
					request: request{
						endpoint: "ExampleString",
						expected: response{
							statusCode: 200,
							body:       "1",
						},
					},
				},
			},
		},
		{
			name: "deactivate works",
			requests: []testRequest{
				&gofailTestRequest{
					requestType: "deactivate",
					request: request{
						endpoint: "ExampleString",
						expected: response{
							statusCode: 204,
							body:       "",
						},
					},
				},
				&gofailTestRequest{
					requestType: "listall",
					request: request{
						expected: response{
							statusCode: 200,
							body:       "ExampleLabels=\nExampleOneLine=\nExampleString=\n",
						},
					},
				},
				&gofailTestRequest{
					requestType: "list",
					request: request{
						endpoint: "ExampleString",
						expected: response{
							statusCode: 404,
							body:       "failed to GET: failpoint: failpoint is disabled\n\n",
						},
					},
				},
				&gofailTestRequest{
					requestType: "count",
					request: request{
						endpoint: "ExampleString",
						expected: response{
							statusCode: 500,
							body:       "failed to GET: failpoint: failpoint is disabled\n",
						},
					},
				},
				&serverTestRequest{
					request: request{
						endpoint: "ExampleFunc",
						expected: response{
							statusCode: 200,
							body:       "\"example\"",
						},
					},
				},
			},
		},
		{
			name: "re-enabling a failpoint resets count",
			requests: []testRequest{
				&gofailTestRequest{
					requestType: "put",
					request: request{
						endpoint: "ExampleString",
						args:     []string{"return(\"new fail string\")"},
						expected: response{
							statusCode: 204,
							body:       "",
						},
					},
				},
				&gofailTestRequest{
					requestType: "count",
					request: request{
						endpoint: "ExampleString",
						expected: response{
							statusCode: 200,
							body:       "0",
						},
					},
				},
			},
		},
		{
			name: "failpoints works as expected",
			requests: []testRequest{
				&gofailTestRequest{
					requestType: "failpoints",
					request: request{
						args: []string{
							"ExampleString=return(\"fail string1\")->return(\"fail string2\")",
							"ExampleOneLine=return(\"def\")",
							"ExampleLabels=return"},
						expected: response{
							statusCode: 204,
							body:       "",
						},
					},
				},
				&gofailTestRequest{
					requestType: "listall",
					request: request{
						expected: response{
							statusCode: 200,
							body: strings.Join([]string{
								"ExampleString=return(\"fail string1\")->return(\"fail string2\")",
								"ExampleOneLine=return(\"def\")",
								"ExampleLabels=return"}, "\n") + "\n",
						},
					},
				},
				&gofailTestRequest{
					requestType: "count",
					request: request{
						endpoint: "ExampleString",
						expected: response{
							statusCode: 200,
							body:       "0",
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
