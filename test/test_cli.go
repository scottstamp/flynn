package main

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"strings"
	"syscall"

	c "github.com/flynn/flynn/Godeps/_workspace/src/github.com/flynn/go-check"
	"github.com/flynn/flynn/Godeps/_workspace/src/golang.org/x/crypto/ssh"
	"github.com/flynn/flynn/cli/config"
	"github.com/flynn/flynn/controller/client"
	ct "github.com/flynn/flynn/controller/types"
	"github.com/flynn/flynn/pkg/random"
)

type CLISuite struct {
	Helper
}

var _ = c.ConcurrentSuite(&CLISuite{})

func (s *CLISuite) TearDownSuite(t *c.C) {
	s.cleanup()
}

func (s *CLISuite) flynn(t *c.C, args ...string) *CmdResult {
	return flynn(t, "/", args...)
}

func (s *CLISuite) newCliTestApp(t *c.C) *cliTestApp {
	app, _ := s.createApp(t)
	stream, err := s.controllerClient(t).StreamJobEvents(app.Name, 0)
	t.Assert(err, c.IsNil)
	return &cliTestApp{app.Name, stream, t}
}

type cliTestApp struct {
	name   string
	stream *controller.JobEventStream
	t      *c.C
}

func (a *cliTestApp) flynn(args ...string) *CmdResult {
	return flynn(a.t, "/", append([]string{"-a", a.name}, args...)...)
}

func (a *cliTestApp) waitFor(events jobEvents) (int64, string) {
	return waitForJobEvents(a.t, a.stream.Events, events)
}

func (a *cliTestApp) sh(cmd string) *CmdResult {
	return a.flynn("run", "sh", "-c", cmd)
}

func (s *CLISuite) TestApp(t *c.C) {
	app := s.newGitRepo(t, "")
	name := random.String(30)
	flynnRemote := fmt.Sprintf("flynn\tssh://git@%s/%s.git (push)", s.clusterConf(t).GitHost, name)

	t.Assert(app.flynn("create", name), Outputs, fmt.Sprintf("Created %s\n", name))
	t.Assert(app.flynn("apps"), OutputContains, name)
	t.Assert(app.git("remote", "-v"), OutputContains, flynnRemote)

	// make sure flynn components are listed
	t.Assert(app.flynn("apps"), OutputContains, "router")

	// flynn delete
	t.Assert(app.flynn("delete", "--yes"), Succeeds)
	t.Assert(app.git("remote", "-v"), c.Not(OutputContains), flynnRemote)
}

// TODO: share with cli/key.go
func formatKeyID(s string) string {
	buf := make([]byte, 0, len(s)+((len(s)-2)/2))
	for i := range s {
		buf = append(buf, s[i])
		if (i+1)%2 == 0 && i != len(s)-1 {
			buf = append(buf, ':')
		}
	}
	return string(buf)
}

func (s *CLISuite) TestKey(t *c.C) {
	app := s.newGitRepo(t, "empty")
	t.Assert(app.flynn("create"), Succeeds)

	t.Assert(app.flynn("key", "add", s.sshKeys(t).Pub), Succeeds)

	// calculate fingerprint
	data, err := ioutil.ReadFile(s.sshKeys(t).Pub)
	t.Assert(err, c.IsNil)
	pubKey, _, _, _, err := ssh.ParseAuthorizedKey(data)
	t.Assert(err, c.IsNil)
	digest := md5.Sum(pubKey.Marshal())
	fingerprint := formatKeyID(hex.EncodeToString(digest[:]))

	t.Assert(app.flynn("key"), OutputContains, fingerprint)

	t.Assert(app.git("commit", "--allow-empty", "-m", "should succeed"), Succeeds)
	t.Assert(app.git("push", "flynn", "master"), Succeeds)

	t.Assert(app.flynn("key", "remove", fingerprint), Succeeds)
	t.Assert(app.flynn("key"), c.Not(OutputContains), fingerprint)

	t.Assert(app.git("commit", "--allow-empty", "-m", "should fail"), Succeeds)
	t.Assert(app.git("push", "flynn", "master"), c.Not(Succeeds))

	t.Assert(app.flynn("delete", "--yes"), Succeeds)
}

func (s *CLISuite) TestPs(t *c.C) {
	app := s.newCliTestApp(t)
	ps := func() []string {
		out := app.flynn("ps")
		t.Assert(out, Succeeds)
		lines := strings.Split(out.Output, "\n")
		return lines[1 : len(lines)-1]
	}
	// empty formation == empty ps
	t.Assert(ps(), c.HasLen, 0)
	t.Assert(app.flynn("scale", "echoer=3"), Succeeds)
	jobs := ps()
	// should return 3 jobs
	t.Assert(jobs, c.HasLen, 3)
	// check job types
	for _, j := range jobs {
		t.Assert(j, Matches, "echoer")
	}
	t.Assert(app.flynn("scale", "echoer=0"), Succeeds)
	t.Assert(ps(), c.HasLen, 0)
}

func (s *CLISuite) TestScale(t *c.C) {
	app := s.newCliTestApp(t)
	t.Assert(app.flynn("scale", "echoer=1"), Succeeds)
	app.waitFor(jobEvents{"echoer": {"up": 1}})
	// should only start the missing two jobs
	t.Assert(app.flynn("scale", "echoer=3"), Succeeds)
	app.waitFor(jobEvents{"echoer": {"up": 2}})
	// should stop all jobs
	t.Assert(app.flynn("scale", "echoer=0"), Succeeds)
	app.waitFor(jobEvents{"echoer": {"down": 3}})
}

func (s *CLISuite) TestRun(t *c.C) {
	app := s.newCliTestApp(t)

	t.Assert(app.flynn("run", "-e", "echo", "hello"), Outputs, "hello\n")
	// drain the events
	app.waitFor(jobEvents{"": {"up": 1, "down": 1}})

	detached := app.flynn("run", "-d", "-e", "echo", "hello")
	t.Assert(detached, Succeeds)
	t.Assert(detached, c.Not(Outputs), "hello\n")

	id := strings.TrimSpace(detached.Output)
	_, jobID := app.waitFor(jobEvents{"": {"up": 1, "down": 1}})
	t.Assert(jobID, c.Equals, id)
	t.Assert(app.flynn("log", id), Outputs, "hello\n")

	// barebones wrapper
	f := func(cmdArgs ...string) *exec.Cmd {
		cmd := exec.Command(args.CLI, append([]string{"-a", app.name}, cmdArgs...)...)
		cmd.Env = append(os.Environ(), "FLYNNRC="+flynnrc)
		cmd.Dir = "/"
		return cmd
	}
	sh := func(cmd string) *exec.Cmd {
		return f("run", "sh", "-c", cmd)
	}

	// test stdin and stderr
	streams := sh("cat 1>&2")
	stdin, err := streams.StdinPipe()
	t.Assert(err, c.IsNil)
	go func() {
		stdin.Write([]byte("goto stderr"))
		stdin.Close()
	}()
	var stderr bytes.Buffer
	var stdout bytes.Buffer
	streams.Stderr = &stderr
	streams.Stdout = &stdout
	t.Assert(streams.Run(), c.IsNil)
	t.Assert(stderr.String(), c.Equals, "goto stderr")
	t.Assert(stdout.String(), c.Equals, "")

	// test exit code
	exit := app.sh("exit 42")
	t.Assert(exit, c.Not(Succeeds))
	if msg, ok := exit.Err.(*exec.ExitError); ok { // there is error code
		code := msg.Sys().(syscall.WaitStatus).ExitStatus()
		t.Assert(code, c.Equals, 42)
	} else {
		t.Fatal("There was no error code!")
	}

	// test signal forwarding
	trap := sh("trap 'echo true' SIGUSR1 && tail -f /dev/null")
	var out bytes.Buffer
	trap.Stdout = &out
	go trap.Run()
	trap.Process.Signal(syscall.SIGUSR1)
	t.Assert(out.String(), c.Equals, "true")
}

func (s *CLISuite) TestEnv(t *c.C) {
	app := s.newCliTestApp(t)
	t.Assert(app.flynn("env", "set", "ENV_TEST=var", "SECOND_VAL=2"), Succeeds)
	t.Assert(app.flynn("env"), OutputContains, "ENV_TEST=var\nSECOND_VAL=2")
	t.Assert(app.flynn("env", "get", "ENV_TEST"), Outputs, "var\n")
	// test that containers do contain the ENV var
	t.Assert(app.sh("echo $ENV_TEST"), Outputs, "var\n")
	t.Assert(app.flynn("env", "unset", "ENV_TEST"), Succeeds)
	t.Assert(app.sh("echo $ENV_TEST"), Outputs, "\n")
}

func (s *CLISuite) TestKill(t *c.C) {
	app := s.newCliTestApp(t)
	t.Assert(app.flynn("scale", "--no-wait", "echoer=1"), Succeeds)
	_, jobID := app.waitFor(jobEvents{"echoer": {"up": 1}})

	t.Assert(app.flynn("kill", jobID), Succeeds)
	_, stoppedID := app.waitFor(jobEvents{"echoer": {"down": 1}})
	t.Assert(stoppedID, c.Equals, jobID)
}

func (s *CLISuite) TestRoute(t *c.C) {
	app := s.newCliTestApp(t)

	// flynn route add http
	route := random.String(32) + ".dev"
	newRoute := app.flynn("route", "add", "http", route)
	t.Assert(newRoute, Succeeds)
	routeID := strings.TrimSpace(newRoute.Output)
	t.Assert(app.flynn("route"), OutputContains, routeID)

	// flynn route remove
	t.Assert(app.flynn("route", "remove", routeID), Succeeds)
	t.Assert(app.flynn("route"), c.Not(OutputContains), routeID)

	// flynn route add tcp
	tcpRoute := app.flynn("route", "add", "tcp")
	t.Assert(tcpRoute, Succeeds)
	routeID = strings.Split(tcpRoute.Output, " ")[0]
	t.Assert(app.flynn("route"), OutputContains, routeID)

	// flynn route remove
	t.Assert(app.flynn("route", "remove", routeID), Succeeds)
	t.Assert(app.flynn("route"), c.Not(OutputContains), routeID)
}

func (s *CLISuite) TestProvider(t *c.C) {
	t.Assert(s.flynn(t, "provider"), OutputContains, "postgres")
}

func (s *CLISuite) TestResource(t *c.C) {
	app := s.newCliTestApp(t)
	t.Assert(app.flynn("resource", "add", "postgres").Output, Matches, `Created resource \w+ and release \w+.`)

	res, err := s.controllerClient(t).AppResourceList(app.name)
	t.Assert(err, c.IsNil)
	t.Assert(res, c.HasLen, 1)
	// the env variables should be set
	t.Assert(app.sh("test -n $FLYNN_POSTGRES"), Succeeds)
	t.Assert(app.sh("test -n $PGUSER"), Succeeds)
	t.Assert(app.sh("test -n $PGPASSWORD"), Succeeds)
	t.Assert(app.sh("test -n $PGDATABASE"), Succeeds)
}

func (s *CLISuite) TestLog(t *c.C) {
	app := s.newCliTestApp(t)
	t.Assert(app.flynn("scale", "--no-wait", "printer=1"), Succeeds)
	_, jobID := app.waitFor(jobEvents{"printer": {"up": 1}})

	t.Assert(app.flynn("log", jobID), OutputContains, "I like to print")

	t.Assert(app.flynn("scale", "printer=0"), Succeeds)
}

func (s *CLISuite) TestCluster(t *c.C) {
	// use a custom flynnrc to avoid disrupting other tests
	file, err := ioutil.TempFile("", "")
	t.Assert(err, c.IsNil)
	flynn := func(cmdArgs ...string) *CmdResult {
		cmd := exec.Command(args.CLI, cmdArgs...)
		cmd.Env = flynnEnv(file.Name())
		return run(t, cmd)
	}

	// cluster add
	t.Assert(flynn("cluster", "add", "-g", "test.example.com:2222", "-p", "KGCENkp53YF5OvOKkZIry71+czFRkSw2ZdMszZ/0ljs=", "test", "https://controller.test.example.com", "e09dc5301d72be755a3d666f617c4600"), Succeeds)
	t.Assert(flynn("cluster"), OutputContains, "test")
	// make sure the cluster is present in the config
	cfg, err := config.ReadFile(file.Name())
	t.Assert(err, c.IsNil)
	t.Assert(cfg.Clusters, c.HasLen, 1)
	t.Assert(cfg.Clusters[0].Name, c.Equals, "test")
	// overwriting should not work
	t.Assert(flynn("cluster", "add", "test", "foo", "bar"), c.Not(Succeeds))
	t.Assert(flynn("cluster"), OutputContains, "test")
	// cluster remove
	t.Assert(flynn("cluster", "remove", "test"), Succeeds)
	t.Assert(flynn("cluster"), c.Not(OutputContains), "test")
	cfg, err = config.ReadFile(file.Name())
	t.Assert(err, c.IsNil)
	t.Assert(cfg.Clusters, c.HasLen, 0)
}

func (s *CLISuite) TestRelease(t *c.C) {
	releaseJSON := []byte(`{
		"env": {"GLOBAL": "FOO"},
		"processes": {
			"echoer": {
				"cmd": ["/bin/echoer"],
				"env": {"ECHOER_ONLY": "BAR"}
			},
			"env": {
				"cmd": ["sh", "-c", "env; while true; do sleep 60; done"],
				"env": {"ENV_ONLY": "BAZ"}
			}
		}
	}`)
	release := &ct.Release{}
	t.Assert(json.Unmarshal(releaseJSON, &release), c.IsNil)

	file, err := ioutil.TempFile("", "")
	t.Assert(err, c.IsNil)
	file.Write(releaseJSON)
	file.Close()

	app := s.newCliTestApp(t)
	t.Assert(app.flynn("release", "add", "-f", file.Name(), testImageURI), Succeeds)

	r, err := s.controller.GetAppRelease(app.name)
	t.Assert(err, c.IsNil)
	t.Assert(r.Env, c.DeepEquals, release.Env)
	t.Assert(r.Processes, c.DeepEquals, release.Processes)

	t.Assert(app.flynn("scale", "--no-wait", "env=1"), Succeeds)
	_, jobID := app.waitFor(jobEvents{"env": {"up": 1}})
	envLog := app.flynn("log", jobID)
	t.Assert(envLog, Succeeds)
	t.Assert(envLog, OutputContains, "GLOBAL=FOO")
	t.Assert(envLog, OutputContains, "ENV_ONLY=BAZ")
	t.Assert(envLog, c.Not(OutputContains), "ECHOER_ONLY=BAR")
}
