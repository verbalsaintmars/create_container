package main

import (
	"context"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/archive"
	flags "github.com/jessevdk/go-flags"
)

var rand uint32

type Options struct {
	Basesrc      string `short:"b" long:"basesrc" description:"base source directory" required:"true" group:"required"`
	Cmd          string `short:"c" long:"cmd" description:"CMD for container"`
	Cname        string `long:"cname" description:"container name"`
	Gid          int    `long:"gid " description:"GID in container"`
	Gname        string `long:"gname" description:"group name" default:"deployer"`
	ImageId      string `long:"imageid" description:"docker image id"`
	InstallJson  string `short:"j" long:"json" description:"install_json.json" required:"true" group:"required"`
	NoproxyHosts string `long:"noproxy" description:"no proxy hosts"`
	Privilege    bool   `long:"priviledge" description:"run container in priviledged mode"`
	Root         bool   `long:"root" description:"run container as root"`
	Tag          string `long:"tag" description:"image tag" default:"latest"`
	Uid          int    `long:"uid" description:"UID in container"`
	Uname        string `long:"uname" description:"user name" default:"deployer"`
	Project      string `short:"p" long:"project" description:"project type" required:"true" group:"required" choice:"higgs" choice:"konrad" choice:"racdb"`
	Workdir      string `short:"w" long:"workdir" description:"working directory"`
	DockerApi    string `long:"apiversion" description:"docker client api version" default:"1.24"`
}

type Project struct {
	Options
	DockerClient struct {
		Ctx    *context.Context
		Client *client.Client
	}
	ImageRepository string
	Image           types.ImageSummary
	Container       container.ContainerCreateCreatedBody
	Shell           string
}

var project = [3]string{"konrad", "higgs", "racdb"}

var home = map[string]string{
	"konrad": `/home/shinto/host`,
	"higgs":  `/home/shinto/host`,
	"racdb":  `/home/shinto/host`}

var shell = map[string]string{
	"zsh":  `/bin/zsh`, // I like zsh better.
	"bash": `/bin/bash`,
	"sh":   `/bin/sh`}

var pgfiles = map[string]string{
	"passwd": `/etc/passwd`,
	"group":  `/etc/group`}

var srcPath = map[string]string{
	"konrad": `compute-konrad-deployer/deployer`,
	"higgs":  `higgs-gateway-appliance-deployer/deployer`,
	"racdb":  `compute-rac-db-deployer/deployer`}

var imageRepository = map[string]string{
	"konrad": fmt.Sprintf("compute-deployer_dev_%s", getUserInfo(name)),
	//"higgs":  fmt.Sprintf("compute-deployer_dev_%s", getUserInfo(name)),
	"higgs": `verbalsaint/o_opengrok`,
	"racdb": fmt.Sprintf("compute-deployer_dev_%s", getUserInfo(name))}

var subDirs = map[string]string{"log": "log", "src": "src"}

var defaultCmd = `tail -f /dev/null`

var noProxyHosts = "localhost,127.0.0.1"

var repoBaseUrl = `http://artifactory-slc.oraclecorp.com/artifactory/opc-delivery-release`

//"dummy.us.oracle.com"
var logstashUrl = "dummy.us.oracle.com"

var logstashPort = 4242

const (
	name = iota
	uid
	gid
)

func check(e error, msg string) {
	if e != nil {
		fmt.Println(msg)
		panic(e)
	}
}

func optParser() *Options {
	var opts Options

	if _, err := flags.Parse(&opts); err != nil {
		if flagsErr, ok := err.(*flags.Error); ok && flagsErr.Type == flags.ErrHelp {
			os.Exit(0)
		} else {
			os.Exit(1)
		}
	}

	if len(opts.Cmd) == 0 {
		opts.Cmd = defaultCmd
	}

	if len(opts.Cname) == 0 {
		t := time.Now()
		opts.Cname = "deployer_" + t.Format("Jan02Mon3456")
	}

	if opts.Gid == 0 {
		opts.Gid = getUserInfo(gid).(int)
	}

	if opts.Uid == 0 {
		opts.Uid = getUserInfo(uid).(int)
	}

	if len(opts.NoproxyHosts) == 0 {
		opts.NoproxyHosts = noProxyHosts
	}

	if len(opts.Workdir) == 0 {
		opts.Workdir = getTempDir()
	}
	return &opts
}

// --- Tools ---
func getUserInfo(t int) interface{} {
	user, err := user.Current()

	check(err, "Can't get current user name.")

	switch t {
	case name:
		return user.Username
	case uid:
		i, _ := strconv.Atoi(user.Uid)
		return i
	case gid:
		i, _ := strconv.Atoi(user.Gid)
		return i
	default:
		return nil
	}
}

func reseed() uint32 {
	return uint32(time.Now().UnixNano() + int64(os.Getpid()))
}

func nextSuffix() string {
	r := rand
	if r == 0 {
		r = reseed()
	}
	r = r*1664525 + 1013904223 // constants from Numerical Recipes
	rand = r
	return strconv.Itoa(int(1e9 + r%1e9))[1:]
}

func getTempDir() (name string) {
	nconflict := 0
	for i := 0; i < 10000; i++ {
		name = "cc_" + nextSuffix()
		_, err := os.Stat(name)
		if os.IsExist(err) {
			if nconflict++; nconflict > 10 {
				rand = reseed()
			}
			continue
		}
		break
	}
	return
}

func extractImageId(id string) string {
	return strings.Split(id, ":")[1]
}

func (p *Project) copyFileFromContainer(from, to string, hook func() string) {
	cli := p.DockerClient.Client
	ctx := *p.DockerClient.Ctx
	fromIo, stat, err := cli.CopyFromContainer(ctx, p.Container.ID, from)
	check(err, "Copy file "+from+" from container "+p.Container.ID+" failed.")

	srcInfo := archive.CopyInfo{
		Path:       from,
		Exists:     true,
		IsDir:      stat.Mode.IsDir(),
		RebaseName: "",
	}
	// untar and copy
	archive.CopyTo(fromIo, srcInfo, to)
	// write uid/gid info
	fd, err := os.OpenFile(to, os.O_APPEND|os.O_WRONLY, 0644)
	fd.WriteString(hook())
	defer fromIo.Close()
	defer fd.Close()
}

func (p *Project) inspectContainerFile(path string) bool {
	cli := p.DockerClient.Client
	ctx := *p.DockerClient.Ctx
	_, err := cli.ContainerStatPath(ctx, p.Container.ID, path)
	if err != nil {
		return false
	}
	return true
}

func (p *Project) checkShell() {
	for k, v := range shell {
		if p.inspectContainerFile(v) {
			p.Shell = k
			return
		}
	}
	panic("No known shell in container available.")
}

// ---End of tools---

func (p *Project) createDockerClient() {
	// setup docker client api
	os.Setenv("DOCKER_API_VERSION", p.DockerApi)

	ctx := context.Background()
	cli, err := client.NewEnvClient()
	check(err, "Create docker client error.")

	p.DockerClient.Ctx = &ctx
	p.DockerClient.Client = cli
}

func (p *Project) getImageId() {
	cli := p.DockerClient.Client
	ctx := *p.DockerClient.Ctx
	ilist, err := cli.ImageList(ctx, types.ImageListOptions{})
	check(err, "Get docker image list error.")

	for _, image := range ilist {
		// if docker image id provided, honor image id
		if strings.Contains(image.ID, p.ImageRepository) {
			p.Image = image
			return
		}
		for _, name := range image.RepoTags {
			tagTmp := strings.Split(name, ":")
			if strings.Contains(tagTmp[0], p.ImageRepository) {
				if strings.Contains(tagTmp[1], p.Tag) {
					p.Image = image
					fmt.Println("debug: in getImageId")
				}
			}
		}
	}
}

// Prepare Config for creating the container
func (p *Project) prepareConfig() *container.Config {
	var config container.Config
	config.Image = extractImageId(p.Image.ID)
	config.Entrypoint = []string{p.Cmd}
	return &config
}

func (p *Project) createContainer() {
	cli := p.DockerClient.Client
	ctx := *p.DockerClient.Ctx
	config := p.prepareConfig()

	if body, err := cli.ContainerCreate(ctx, config, nil, nil, p.Cname); err != nil {
		fmt.Println("Create container failed with error: " + err.Error())
		panic("Create Container failed")
	} else {
		p.Container = body
	}

}

func (p *Project) removeContainer() {
	cli := p.DockerClient.Client
	ctx := *p.DockerClient.Ctx
	var removeConfig types.ContainerRemoveOptions = types.ContainerRemoveOptions{false, false, true}
	if err := cli.ContainerRemove(ctx, p.Container.ID, removeConfig); err != nil {
		fmt.Println("Remove container ID: " + p.Container.ID + " failed")
		panic("Remove container failed")
	}
}

func (p *Project) createTmpDir() {
	currentPath, _ := os.Getwd()
	workingPath := filepath.Join(currentPath, p.Workdir)
	p.Workdir = workingPath
	for _, v := range subDirs {
		os.MkdirAll(filepath.Join(workingPath, v), os.FileMode(0755))
	}
	// Change to workdir
	os.Chdir(p.Workdir)
}

func (p *Project) rewriteUidGid() {
	hooks := map[string]func() string{
		"passwd": func() string {
			//"{user}:x:{uid}:{gid}::{home}:{shell}"
			return p.Uname + ":x:" + strconv.Itoa(p.Uid) + ":" + strconv.Itoa(p.Gid) + "::" + home[p.Project] + ":" + shell[p.Shell]
		},
		"group": func() string {
			//"{group}:x:{gid}:"
			return p.Gname + ":x:" + strconv.Itoa(p.Gid) + ":"
		}}
	for k, v := range pgfiles {
		p.copyFileFromContainer(v, k, hooks[k])
	}
}

func (p *Project) prepare(opts *Options) {
	oValue := reflect.ValueOf(*opts)
	pValue := reflect.ValueOf(p).Elem()
	pOvalue := pValue.Field(0)
	for i := 0; i < pOvalue.NumField(); i++ {
		f := pOvalue.Field(i)
		f.Set(reflect.Value(oValue.Field(i)))
	}
	// setup image repository default name
	if len(p.ImageId) != 0 {
		p.ImageRepository = p.ImageId
	} else {
		p.ImageRepository = imageRepository[p.Project]
	}
}

func (p *Project) run() {
	p.createTmpDir()
	p.createDockerClient()
	p.getImageId()
	p.createContainer()
	p.checkShell()
	p.rewriteUidGid()
	fmt.Println(p.Shell)
}

func main() {
	var opts = optParser()
	var project Project
	project.prepare(opts)
	project.run()
}
