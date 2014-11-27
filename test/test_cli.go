package main

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io/ioutil"
	"os/exec"
	"strings"

	c "github.com/flynn/flynn/Godeps/_workspace/src/github.com/flynn/go-check"
	"github.com/flynn/flynn/Godeps/_workspace/src/golang.org/x/crypto/ssh"
	"github.com/flynn/flynn/controller/client"
	"github.com/flynn/flynn/pkg/random"
)

type CLISuite struct {
	Helper
	ssh *sshData
}

var _ = c.ConcurrentSuite(&CLISuite{})

func (s *CLISuite) SetUpSuite(t *c.C) {
	var err error
	s.ssh, err = genSSHKey()
	t.Assert(err, c.IsNil)
}

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

func (s *CLISuite) TestApp(t *c.C) {
	app := newGitRepo(t, "", s.ssh)
	name := random.String(30)
	t.Assert(app.flynn("create", name), Outputs, fmt.Sprintf("Created %s\n", name))
	t.Assert(app.flynn("apps"), OutputContains, name)
	// git repo should include a push remote labeled flynn
	t.Assert(app.git("remote", "-v").Output, Matches, `(?m)^flynn\t.+ \(push\)$`)
	// make sure flynn components are listed
	t.Assert(app.flynn("apps"), OutputContains, "router")
	// flynn delete
	t.Assert(app.flynn("delete", "--yes"), Succeeds)
	t.Assert(app.git("remote", "-v").Output, c.Not(Matches), `(?m)^flynn\t.+ \(push\)$`)
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
	t.Assert(s.flynn(t, "key", "add", s.ssh.Pub), Succeeds)

	// calculate fingerprint
	data, err := ioutil.ReadFile(s.ssh.Pub)
	t.Assert(err, c.IsNil)
	pubKey, _, _, _, err := ssh.ParseAuthorizedKey(data)
	t.Assert(err, c.IsNil)
	digest := md5.Sum(pubKey.Marshal())
	fingerprint := formatKeyID(hex.EncodeToString(digest[:]))

	t.Assert(s.flynn(t, "key"), OutputContains, fingerprint)
	t.Assert(s.flynn(t, "key", "remove", fingerprint), Succeeds)
	t.Assert(s.flynn(t, "key"), c.Not(OutputContains), fingerprint)
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
	app.waitFor(jobEvents{"echoer": {"up": 3}})
	jobs := ps()
	// should return 3 jobs
	t.Assert(jobs, c.HasLen, 3)
	// check job types
	for _, j := range jobs {
		t.Assert(j, Matches, "echoer")
	}
	t.Assert(app.flynn("scale", "echoer=0"), Succeeds)
	app.waitFor(jobEvents{"echoer": {"down": 3}})
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

func (s *CLISuite) TestEnv(t *c.C) {
	app := s.newCliTestApp(t)
	t.Assert(app.flynn("env", "set", "ENV_TEST=var", "SECOND_VAL=2"), Succeeds)
	t.Assert(app.flynn("env"), OutputContains, "ENV_TEST=var\nSECOND_VAL=2")
	t.Assert(app.flynn("env", "get", "ENV_TEST"), Outputs, "var\n")
	// test that containers do contain the ENV var
	t.Assert(app.flynn("run", "-e", "bash", "--", "-c", "echo $ENV_TEST"), Outputs, "var\n")
	t.Assert(app.flynn("env", "unset", "ENV_TEST"), Succeeds)
	t.Assert(app.flynn("run", "-e", "bash", "--", "-c", "echo $ENV_TEST"), Outputs, "\n")
}

func (s *CLISuite) TestKill(t *c.C) {
	app := s.newCliTestApp(t)
	t.Assert(app.flynn("scale", "echoer=1"), Succeeds)
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
	// the env variable should be set
	t.Assert(app.flynn("run", "-e", "bash", "--", "-c", "echo $FLYNN_POSTGRES"), c.Not(Outputs), "\n")
}

func (s *CLISuite) TestLog(t *c.C) {
	app := s.newCliTestApp(t)
	t.Assert(app.flynn("scale", "printer=1"), Succeeds)
	_, jobID := app.waitFor(jobEvents{"printer": {"up": 1}})

	t.Assert(app.flynn("log", jobID), OutputContains, "I like to print")

	t.Assert(app.flynn("scale", "printer=0"), Succeeds)
	app.waitFor(jobEvents{"printer": {"down": 1}})
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
	// overwriting should not work
	t.Assert(flynn("cluster", "add", "test", "foo", "bar"), c.Not(Succeeds))
	t.Assert(flynn("cluster"), OutputContains, "test")
	// cluster remove
	t.Assert(flynn("cluster", "remove", "test"), Succeeds)
	t.Assert(flynn("cluster"), c.Not(OutputContains), "test")
}
