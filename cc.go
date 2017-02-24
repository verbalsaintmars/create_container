package main

import (
	"fmt"
	"os"
	"os/user"
	"strconv"
	"time"

	flags "github.com/jessevdk/go-flags"
)

var rand uint32

var project = [3]string{"konrad", "higgs", "racdb"}

var home = map[string]string{
	"konrad": `/home/shinto/host`,
	"higgs":  `/home/shinto/host`,
	"racdb":  `/home/shinto/host`}

var shell = map[string]string{
	"bash": `/bin/bash`,
	"zsh":  `/bin/zsh`}

var srcPath = map[string]string{
	"konrad": `compute-konrad-deployer/deployer`,
	"higgs":  `higgs-gateway-appliance-deployer/deployer`,
	"racdb":  `compute-rac-db-deployer/deployer`}

var imageRepository = map[string]string{
	"konrad": fmt.Sprintf("compute-deployer_dev_%s", getUserInfo(name)),
	"higgs":  fmt.Sprintf("compute-deployer_dev_%s", getUserInfo(name)),
	"racdb":  fmt.Sprintf("compute-deployer_dev_%s", getUserInfo(name))}

var subDirs = map[string]string{"log": "log", "src": "src"}

var defaultCmd = `tail -f /dev/null`

var noProxyHosts = "localhost,127.0.0.1"

//"{user}:x:{uid}:{gid}::{home}:{shell}"
var passwdFmt = "%s:x:%s:%s::%s:%s"

//"{group}:x:{gid}:"
var groupFmt = "%s:x:%s:"

//":{UID}:{GID}:"
var passwdRegex = ":%s:%s:"

//":{GID}:"
var groupRegex = ":%s:"

var repoBaseUrl = `http://artifactory-slc.oraclecorp.com/artifactory/opc-delivery-release`

//"dummy.us.oracle.com"
var logstashUrl = "dummy.us.oracle.com"

var logstashPort = 4242

const (
	name = iota
	uid
	gid
)

func getUserInfo(t int) interface{} {
	user, ok := user.Current()

	if ok != nil {
		fmt.Println(ok)
		panic("Can't get current user name.")
	}

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
		name = nextSuffix()
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

func init() {
	fmt.Println("in init")
}

func optParser() {
	var opts struct {
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
		Project      string `short:"p" long:"project" description:"project type" required:"true" group:"required"`
		Workdir      string `short:"w" long:"workdir" description:"working directory"`
	}

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
		opts.Cname = t.Format(time.RFC3339)
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

	fmt.Println(opts.Gid)
	fmt.Println(opts.Workdir)
}

func main() {
	optParser()
}
