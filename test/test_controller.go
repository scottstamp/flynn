package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/cupcake/jsonschema"
	c "github.com/flynn/flynn/Godeps/_workspace/src/gopkg.in/check.v1"
	"github.com/flynn/flynn/cli/config"
	"github.com/flynn/flynn/controller/client"
)

type ControllerSuite struct {
	client      *controller.Client
	config      *config.Cluster
	schemaPaths []string
	schemaCache map[string]*jsonschema.Schema
}

var _ = c.Suite(&ControllerSuite{})

func (s *ControllerSuite) SetUpSuite(t *c.C) {
	conf, err := config.ReadFile(flynnrc)
	t.Assert(err, c.IsNil)
	t.Assert(conf.Clusters, c.HasLen, 1)
	s.config = conf.Clusters[0]

	s.client = newControllerClient(t, s.config)

	schemaPaths := make([]string, 0, 0)
	walkFn := func(path string, info os.FileInfo, err error) error {
		if !info.IsDir() {
			schemaPaths = append(schemaPaths, path)
		}
		return nil
	}
	schemaRoot, err := filepath.Abs(filepath.Join("..", "website", "schema"))
	t.Assert(err, c.IsNil)
	filepath.Walk(schemaRoot, walkFn)

	s.schemaCache = make(map[string]*jsonschema.Schema, len(schemaPaths))
	for _, path := range schemaPaths {
		file, err := os.Open(path)
		t.Assert(err, c.IsNil)
		schema := &jsonschema.Schema{Cache: s.schemaCache}
		cacheKey := "https://flynn.io/schema" + strings.TrimPrefix(path, schemaRoot)
		s.schemaCache[cacheKey] = schema
		err = json.NewDecoder(file).Decode(&s)
		t.Assert(err, c.IsNil)
		file.Close()
	}
	for _, schema := range s.schemaCache {
		schema.ResolveRefs(true)
	}
}

type controllerExampleRequest struct {
	Method  string            `json:"method,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body,omitempty"`
}

type controllerExampleResponse struct {
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body,omitempty"`
}

type controllerExample struct {
	Request  controllerExampleRequest  `json:"request,omitempty"`
	Response controllerExampleResponse `json:"response,omitempty"`
}

func (s *ControllerSuite) generateControllerExamples(t *c.C) map[string]*controllerExample {
	controllerDomain := strings.TrimPrefix(s.config.URL, "https://")
	examplesCmd := exec.Command(args.ControllerExamples)
	env := os.Environ()
	env = append(env, fmt.Sprintf("CONTROLLER_DOMAIN=%s", controllerDomain))
	env = append(env, fmt.Sprintf("CONTROLLER_KEY=%s", s.config.Key))
	examplesCmd.Env = env

	out, err := examplesCmd.Output()
	t.Assert(err, c.IsNil)
	dec := json.NewDecoder(bytes.NewReader(out))
	var examples map[string]*controllerExample
	err = dec.Decode(&examples)
	if err == io.EOF {
		err = nil
	}
	t.Assert(err, c.IsNil)
	return examples
}

func (s *ControllerSuite) TestExampleOutput(t *c.C) {
	examples := s.generateControllerExamples(t)
	for key, data := range examples {
		cacheKey := "https://flynn.io/schema/examples/controller/" + key + ".json"
		schema := s.schemaCache[cacheKey]
		t.Assert(schema, c.Not(c.IsNil))
		errs := schema.Validate(data)
		t.Assert(len(errs), c.Equals, 0)
	}

	badExample := controllerExample{
		Request: controllerExampleRequest{
			Method: "POST",
			URL:    "/apps",
			Headers: map[string]string{
				"Authorization": "Basic OmMwZmVlYTU4OTRiYTg5ZDJlMDQyMWFkYmQzMWM3YWE2",
				"Content-Type":  "application/json",
			},
			Body: `{"name":123,"protected":false}`,
		},
		Response: controllerExampleResponse{
			Headers: map[string]string{
				"Content-Type": "application/json; charset=UTF-8",
			},
			Body: `{"id":"17039fd127124db6a24ad2636dc0947a","name":"my-app-1416409311383044482","protected":false,"created_at":"2014-11-19T15:01:51.536617Z","updated_at":"2014-11-19T15:01:51.536617Z"}`,
		},
	}

	schema := s.schemaCache["https://flynn.io/schema/examples/controller/app_create.json"]
	errs := schema.Validate(badExample)
	t.Assert(len(errs), c.Equals, 0)

	dec := json.NewDecoder(strings.NewReader(badExample.Request.Body))
	fmt.Println(badExample.Request.Body)
	var app interface{}
	err := dec.Decode(&app)
	t.Assert(err, c.IsNil)
	schema = s.schemaCache["https://flynn.io/schema/controller/app.json"]
	errs = schema.Validate(app)
	fmt.Println(errs)
	t.Assert(len(errs), c.Equals, 0)
}
