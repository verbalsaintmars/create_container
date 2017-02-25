package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/archive"
	flags "github.com/jessevdk/go-flags"
)

var rand uint32

type Options struct {
	BaseDir      string `short:"b" long:"basedir" description:"base source directory" required:"true" group:"required"`
	Cmd          string `short:"c" long:"cmd" description:"CMD for container" default:"tail -f /dev/null"`
	Cname        string `long:"cname" description:"container name"`
	Gid          int    `long:"gid " description:"GID in container"`
	Gname        string `long:"gname" description:"group name" default:"deployer"`
	ImageId      string `long:"imageid" description:"docker image id"`
	InstallJson  string `short:"j" long:"json" description:"install_json.json" required:"true" group:"required"`
	NoproxyHosts string `long:"noproxy" description:"no proxy hosts" default:"localhost,127.0.0.1"`
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
	Container       container.ContainerCreateCreatedBody // set in createContainer()
	Shell           string                               // set in checkShell()
	SourceDir       string                               // set in setSourceDir()
}

var project = [3]string{"konrad", "higgs", "racdb"}

var home = map[string]string{
	"konrad": `/home/shinto/host`,
	"higgs":  `/home/shinto/host`,
	"racdb":  `/home/shinto/host`}

var shell = map[string]string{
	"zsh":  `/bin/zsh`,
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
	"higgs":  fmt.Sprintf("compute-deployer_dev_%s", getUserInfo(name)),
	"racdb":  fmt.Sprintf("compute-deployer_dev_%s", getUserInfo(name))}

var subDirs = map[string]string{"log": "log", "src": "src"}

var repoBaseUrl = `http://artifactory-slc.oraclecorp.com/artifactory/opc-delivery-release`

//"dummy.us.oracle.com"
var logstashUrl = `dummy.us.oracle.com`

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

	if len(opts.Workdir) == 0 {
		currentPath, _ := os.Getwd()
		workingPath := filepath.Join(currentPath, getTempDir())
		opts.Workdir = workingPath
	} else {
		opts.Workdir = filepath.Join(opts.Workdir)
	}

	opts.BaseDir, _ = filepath.Abs(opts.BaseDir)
	opts.InstallJson, _ = filepath.Abs(opts.InstallJson)

	return &opts
}

// --- Tools ---

func extractClient(p *Project) (cli *client.Client, ctx *context.Context) {
	cli = p.DockerClient.Client
	ctx = p.DockerClient.Ctx
	return
}
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
	cli, ctx := extractClient(p)
	fromIo, stat, err := cli.CopyFromContainer(*ctx, p.Container.ID, from)
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
	cli, ctx := extractClient(p)
	_, err := cli.ContainerStatPath(*ctx, p.Container.ID, path)
	if err != nil {
		return false
	}
	return true
}

func (p *Project) checkShell() {
	// prefer zsh first
	shellPriority := []string{"zsh", "bash", "sh"}
	availableShell := make(map[string]bool)

	for k, v := range shell {
		if p.inspectContainerFile(v) {
			availableShell[k] = true
		}
	}

	if len(availableShell) != 0 {
		for _, s := range shellPriority {
			_, ok := availableShell[s]
			if ok {
				p.Shell = s
				return
			}
		}
	}
	panic("No proper shell in container available.")
}

func (p *Project) setSourceDir() {
	srcDir := filepath.Join(p.BaseDir, srcPath[p.Project])
	stat, err := os.Stat(srcDir)
	check(err, "Project "+p.Project+" source does not exist.")
	if !stat.IsDir() {
		panic("Source " + srcDir + "is not a directory")
	}
	p.SourceDir = srcDir
}

func (p *Project) printUsage() {
	execCmd := "docker exec -it %s %s"
	stopCmd := "docker stop %s"
	rmCmd := "docker rm %s"
	lsCmd := "docker ps -a"
	stopallCmd := `docker stop $(docker ps -a -q)`
	rmallCmd := `docker rm $(docker ps -a -q)`
	logLoc := filepath.Join(p.Workdir, "log")
	srcLoc := p.SourceDir
	fmt.Println("Access container: " + fmt.Sprintf(execCmd, p.Container.ID[:12], shell[p.Shell]))
	fmt.Println("Stop container: " + fmt.Sprintf(stopCmd, p.Container.ID[:12]))
	fmt.Println("Remove container: " + fmt.Sprintf(rmCmd, p.Container.ID[:12]))
	fmt.Println("List all containers: " + lsCmd)
	fmt.Println("Stop all containers: " + stopallCmd)
	fmt.Println("Remove all containers: " + rmallCmd)
	fmt.Println("Log location: " + logLoc)
	fmt.Println("Source location: " + srcLoc)
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

func (p *Project) setImageId() {
	cli, ctx := extractClient(p)
	ilist, err := cli.ImageList(*ctx, types.ImageListOptions{})
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
				}
			}
		}
	}
	defer func() {
		if p.Image.ID == "" {
			panic("No proper Image found in this host.")
		}
	}()
}

func (p *Project) prepareHostConfig(init bool) *container.HostConfig {
	if init {
		return nil
	}

	var hostConfig container.HostConfig

	var bindOptions mount.BindOptions
	bindOptions.Propagation = mount.PropagationRPrivate

	var logMount mount.Mount
	logMount.Type = mount.TypeBind
	logMount.Source = filepath.Join(p.Workdir, "log")
	logMount.Target = `/var/log/deployer`
	logMount.ReadOnly = false
	logMount.BindOptions = &bindOptions
	logbind := fmt.Sprintf("%s:%s", logMount.Source, logMount.Target)

	var srcMount mount.Mount
	srcMount.Type = mount.TypeBind
	srcMount.Source = p.SourceDir
	srcMount.Target = `/home/shinto/deployer`
	srcMount.ReadOnly = false
	srcMount.BindOptions = &bindOptions
	srcbind := fmt.Sprintf("%s:%s", srcMount.Source, srcMount.Target)

	var hostMount mount.Mount
	hostMount.Type = mount.TypeBind
	hostMount.Source = p.Workdir
	hostMount.Target = `/home/shinto/host`
	hostMount.ReadOnly = false
	hostMount.BindOptions = &bindOptions
	hostbind := fmt.Sprintf("%s:%s", hostMount.Source, hostMount.Target)

	var passwdMount mount.Mount
	passwdMount.Type = mount.TypeBind
	passwdMount.Source = filepath.Join(p.Workdir, "passwd")
	passwdMount.Target = pgfiles["passwd"]
	passwdMount.ReadOnly = false
	passwdMount.BindOptions = &bindOptions
	passwdbind := fmt.Sprintf("%s:%s", passwdMount.Source, passwdMount.Target)

	var groupMount mount.Mount
	groupMount.Type = mount.TypeBind
	groupMount.Source = filepath.Join(p.Workdir, "group")
	groupMount.Target = pgfiles["group"]
	groupMount.ReadOnly = false
	groupMount.BindOptions = &bindOptions
	groupbind := fmt.Sprintf("%s:%s", groupMount.Source, groupMount.Target)

	hostConfig.Binds = []string{
		logbind,
		srcbind,
		hostbind,
		passwdbind,
		groupbind}
	hostConfig.AutoRemove = true
	hostConfig.Privileged = p.Privilege
	hostConfig.Mounts = []mount.Mount{
		logMount,
		srcMount,
		hostMount,
		passwdMount,
		passwdMount}
	return &hostConfig
}

// Prepare Config for creating the container
func (p *Project) prepareConfig(init bool) *container.Config {
	var config container.Config
	config.Image = extractImageId(p.Image.ID)
	config.Entrypoint = strings.Split(p.Cmd, " ")

	if init {
		return &config
	}
	// run as root, 0:0
	config.User = `0:0`

	// setup env
	envStr := make([]string, 4)
	if p.Root {
		envStr = append(envStr, "C_FORCE_ROOT=1")
	}
	envStr = append(envStr, "no_proxy="+p.NoproxyHosts)
	config.Env = envStr

	return &config
}

func (p *Project) createContainer(init bool) {
	cli, ctx := extractClient(p)
	config := p.prepareConfig(init)
	hostConfig := p.prepareHostConfig(init)
	body, err := cli.ContainerCreate(*ctx, config, hostConfig, nil, p.Cname)
	check(err, "Create container failed.")
	p.Container = body
}

func (p *Project) removeContainer() {
	cli, ctx := extractClient(p)
	var removeConfig types.ContainerRemoveOptions = types.ContainerRemoveOptions{false, false, true}
	err := cli.ContainerRemove(*ctx, p.Container.ID, removeConfig)
	check(err, "Remove container: "+p.Container.ID+" failed")
}

func (p *Project) startContainer() {
	cli, ctx := extractClient(p)
	options := types.ContainerStartOptions{}
	err := cli.ContainerStart(*ctx, p.Container.ID, options)
	check(err, "Start container failed.")
}

func (p *Project) createWorkDir() {
	for _, v := range subDirs {
		os.MkdirAll(filepath.Join(p.Workdir, v), os.FileMode(0755))
	}
	// Change to workdir
	os.Chdir(p.Workdir)
}

func (p *Project) rewriteUidGid() {
	hooks := map[string]func() string{
		"passwd": func() string {
			//"{user}:x:{uid}:{gid}::{home}:{shell}"
			return p.Uname + ":x:" + strconv.Itoa(p.Uid) + ":" + strconv.Itoa(p.Gid) + "::" + home[p.Project] + ":" + shell[p.Shell] + "\n"
		},
		"group": func() string {
			//"{group}:x:{gid}:"
			return p.Gname + ":x:" + strconv.Itoa(p.Gid) + ":" + "\n"
		}}
	for k, v := range pgfiles {
		p.copyFileFromContainer(v, k, hooks[k])
	}
}

func (p *Project) touchRepoconfigJson() {
	type (
		Repository struct {
			Repo_base_url string `json:"repo_base_url"`
		}
		Logstash struct {
			Host string `json:"host"`
			Port int    `json:"port"`
		}
		RepoJson struct {
			Repo Repository `json:"repository"`
			Log  Logstash   `json:"logstash"`
		}
	)

	rj := &RepoJson{
		Repo: Repository{Repo_base_url: repoBaseUrl},
		Log:  Logstash{Host: logstashUrl, Port: logstashPort}}
	mrj, err := json.Marshal(rj)
	check(err, "Marshal json file failed.")
	fd, err := os.Create("repoconfig.json")
	check(err, "Create repoconfig.json failed.")
	_, err = fd.Write(mrj)
	check(err, "Write to repoconfig.json error.")
	defer fd.Close()
}

func (p *Project) copyInstallJson() {
	hostFd, err := os.Open(p.InstallJson)
	check(err, "Open provided install_json.json failed.")
	defer hostFd.Close()

	installJsonFile := filepath.Join(p.Workdir, "install_json.json")
	containerFd, err := os.Create(installJsonFile)
	check(err, "Create install_json.json in workdir failed.")
	defer func() {
		err := containerFd.Close()
		check(err, "Close install_json.json in workdir failed.")
		containerFd.Close()
	}()

	_, err = io.Copy(containerFd, hostFd)
	check(err, "Copy install_json.json failed.")
	containerFd.Sync()
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

	// check install_json.json exist
	_, err := os.Stat(p.InstallJson)
	check(err, "Install json file "+p.InstallJson+" doesn't exist.")
}

func (p *Project) run() {
	p.createDockerClient()
	// set p.Image
	p.setImageId()
	p.setSourceDir()
	p.createWorkDir()
	p.createContainer(true)
	p.checkShell()
	p.rewriteUidGid()
	p.removeContainer()
	p.touchRepoconfigJson()
	p.copyInstallJson()
	p.createContainer(false)
	p.startContainer()
	p.printUsage()
}

func main() {
	var opts = optParser()
	var project Project
	project.prepare(opts)
	project.run()
}
