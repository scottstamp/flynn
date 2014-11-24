package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	c "github.com/flynn/flynn/Godeps/_workspace/src/gopkg.in/check.v1"
	"github.com/flynn/flynn/cli/config"
	"github.com/flynn/flynn/controller/client"
)

type ControllerSuite struct {
	client *controller.Client
	config *config.Cluster
}

var _ = c.Suite(&ControllerSuite{})

func (s *ControllerSuite) SetUpSuite(t *c.C) {
	conf, err := config.ReadFile(flynnrc)
	t.Assert(err, c.IsNil)
	t.Assert(conf.Clusters, c.HasLen, 1)
	s.config = conf.Clusters[0]

	s.client = newControllerClient(t, s.config)
}

type controllerExample struct {
	Request struct {
		Method  string            `json:"method,omitempty"`
		URL     string            `json:"url,omitempty"`
		Headers map[string]string `json:"headers,omitempty"`
		Body    string            `json:"body,omitempty"`
	} `json:"request,omitempty"`
	Response struct {
		Headers map[string]string `json:"headers,omitempty"`
		Body    string            `json:"body,omitempty"`
	} `json:"response,omitempty"`
}

func (s *ControllerSuite) generateControllerExamples(t *c.C) []byte {
	controllerDomain := strings.TrimPrefix(s.config.URL, "https://")
	examplesCmd := exec.Command(args.ControllerExamples)
	env := os.Environ()
	env = append(env, fmt.Sprintf("CONTROLLER_DOMAIN=%s", controllerDomain))
	env = append(env, fmt.Sprintf("CONTROLLER_KEY=%s", s.config.Key))
	examplesCmd.Env = env

	out, err := examplesCmd.Output()
	t.Assert(err, c.IsNil)
	return out
}

func (s *ControllerSuite) TestExampleOutput(t *c.C) {
	out := s.generateControllerExamples(t)
	dec := json.NewDecoder(bytes.NewReader(out))
	var examples map[string]*controllerExample
	err := dec.Decode(&examples)
	if err == io.EOF {
		err = nil
	}
	t.Assert(err, c.IsNil)
	// TODO: Validate each example against example schemas
}
