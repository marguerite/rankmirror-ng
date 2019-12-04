package main

import (
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"net/url"
	"regexp"
	"time"

	"github.com/tatsushid/go-fastping"
)

type MirrorList []Mirror

type Mirror struct {
	Name        string
	Raw         string
	URI         *url.URL
	Distro      string
	Version     string
	GeoLocation string
	IPv6        bool
}

func (m Mirror) Ping() time.Duration {
	var t time.Duration
	protocol := "ip4:icmp"
	pinger := fastping.NewPinger()
	if m.IPv6 {
		protocol = "ip6:icmp"
	}

	ra, err := net.ResolveIPAddr(protocol, m.URI.Host)
	if err != nil {
		log.Println(err)
		return t
	}
	pinger.AddIPAddr(ra)
	pinger.OnRecv = func(addr *net.IPAddr, rtt time.Duration) {
		t = rtt
	}
	pinger.OnIdle = func() {}
	err = pinger.Run()
	if err != nil {
		log.Fatal(err)
	}
	return t
}

func contains(a []string, x string) bool {
	for _, v := range a {
		if x == v {
			return true
		}
	}
	return false
}

func osInfo() (id, version string) {
	f, e := ioutil.ReadFile("os-release")
	if e != nil {
		log.Println("can't open /etc/os-release")
		log.Fatal(e)
	}

	SUPPORTED_OS := []string{"tumbleweed", "leap", "fedora", "arch"}

	id_r := regexp.MustCompile(`(?m)^ID=("opensuse-)?([^"]+)(")?$`)
	ver_r := regexp.MustCompile(`(?m)^VERSION_ID=(")?([^"]+)(")?$`)

	id = id_r.FindStringSubmatch(string(f))[2]

	if !contains(SUPPORTED_OS, id) {
		log.Fatal("Unsupported OS: " + id)
	}

	version = "0.0"
	if id == "leap" || id == "fedora" {
		version = ver_r.FindStringSubmatch(string(f))[2]
	}

	return id, version
}

func (m Mirror) FullURL() string {
	ma := make(map[string]string)
	ma["leap"] = "/distribution/leap/" // 15.0/repo/oss
	ma["tumbleweed"] = "/tumbleweed/repo/oss/"
	ma["fedora"] = "/releases/" // 28/Everything/x86_64/os/
	ma["arch"] = "/core/os/x86_64/"

	url := m.Raw + ma[m.Distro]

	if m.Distro == "leap" {
		url += m.Version + "/repo/oss/"
	}

	if m.Distro == "fedora" {
		url += m.Version + "/Everything/x86_64/os/"
	}

	return url
}

func main() {
	var geo string
	var ipv6 bool

	flag.StringVar(&geo, "geo", "china", "geo location of the mirror.")
	flag.BoolVar(&ipv6, "ipv6", false, "check ipv6 only.")

	flag.Parse()

	distro, version := osInfo()
	raw := "https://mirrors.aliyun.com/opensuse"
	uri, _ := url.Parse(raw)
	m := Mirror{"阿里云", raw, uri, distro, version, geo, ipv6}
	fmt.Println(m.Ping())
}
