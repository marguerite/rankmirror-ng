package main

import (
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"

	geoip2 "github.com/oschwald/geoip2-golang"
	"github.com/tatsushid/go-fastping"
	yaml "gopkg.in/yaml.v2"
)

func readMirrorList(file string) (MirrorList, error) {
	mirrorlist := MirrorList{}

	b, err := ioutil.ReadFile(file)
	if err != nil {
		return mirrorlist, err
	}

	err = yaml.Unmarshal(b, &mirrorlist)
	if err != nil {
		return mirrorlist, err
	}

	return mirrorlist, nil
}

type MirrorList []Mirror

func (mirrorlist *MirrorList) preload(force bool) {
	for i := range *mirrorlist {
		err := (*mirrorlist)[i].preload(force)
		if err != nil {
			fmt.Println(err)
			continue
		}
	}
}

type Mirror struct {
	Name             string        `yaml:"name"`
	Raw              string        `yaml:"raw"`
	Distro           string        `yaml: "distro"`
	Version          []string      `yaml: "version"`
	Country          string        `yaml: "country"`
	Latitude         float64       `yaml: "latitude"`
	Longitude        float64       `yaml: "longitude"`
	PhysicalDistance string        `yaml: "distance"`
	RouteLength      string        `yaml: "route"`
	PingSpeed        time.Duration `yaml: "ping"`
	DownloadSpeed    string        `yaml: "download"`
	IPv6             bool          `yaml: "ipv6"`
}

func (m *Mirror) preload(force bool) error {
	// Raw is a must field
	if len(m.Raw) == 0 {
		return fmt.Errorf("raw field is required: %v", *m)
	}

	if len(m.Name) == 0 {
		re := regexp.MustCompile(`([\.\/])?(\w+)\.\w+($|\/)`)
		m.Name = strings.Title(re.FindStringSubmatch(m.Raw)[2])
	}

	if len(m.Distro) == 0 {
		re := regexp.MustCompile(`\/(\w+)$`)
		if re.MatchString(m.Raw) {
			m.Distro = re.FindStringSubmatch(m.Raw)[1]
		} else {
			return fmt.Errorf("distro field is empty and can not combine one from raw field: %v", *m)
		}
	}

	if len(m.Version) == 0 {
		m.Version = probeDistroVersions(m.Distro, m.Raw)
	}

	if len(m.Country) == 0 {
		var err error
		m.Country, m.Latitude, m.Longitude, err = probeGeoLocation(m.Raw)
		if err != nil {
			return fmt.Errorf("probeGeoLocation failed: %v, %v", err, *m)
		}
	}

	if m.Latitude == 0 || m.Longitude == 0 {
		var err error
		_, m.Latitude, m.Longitude, err = probeGeoLocation(m.Raw)
		if err != nil {
			return fmt.Errorf("probeGeoLocation failed: %v, %v", err, *m)
		}
	}

	return nil
}

func probeDistroVersions(distro, raw string) []string {
	versions := []string{}
	paths := []string{}
	suffix := ""

	switch distro {
	case "opensuse":
		paths = []string{"distribution/leap/15.1", "distribution/leap/15.2", "distribution/leap/15.3", "tumbleweed"}
		suffix = "repo/oss"
	}

	timeout := time.Duration(3 * time.Second)

	for _, p := range paths {
		// goroutine?
		uri, _ := url.Parse(raw)
		uri.Path = path.Join(uri.Path, p, suffix)
		c := &http.Client{Timeout: timeout}
		// redirect means it's there for Aliyun
		if strings.Contains(raw, "aliyun.com") {
			c.CheckRedirect = func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }
		}
		resp, err := c.Get(uri.String())
		if err != nil || resp.StatusCode != 404 {
			versions = append(versions, p)
		}
	}

	return versions
}

func probeGeoLocation(raw string) (string, float64, float64, error) {
	uri, _ := url.Parse(raw)
	ra, err := net.ResolveIPAddr("ip4", uri.Host)
	if err != nil {
		return "", 0, 0, err
	}
	ip := net.ParseIP(ra.String())

	db, err := geoip2.Open("GeoLite2-City.mmdb")
	if err != nil {
		return "", 0, 0, err
	}
	defer db.Close()

	record, err := db.City(ip)
	if err != nil {
		return "", 0, 0, err
	}
	return record.Country.Names["en"], record.Location.Latitude, record.Location.Longitude, nil
}

func (m Mirror) Ping() time.Duration {
	var t time.Duration
	protocol := "ip4:icmp"
	pinger := fastping.NewPinger()
	if m.IPv6 {
		protocol = "ip6:icmp"
	}

	uri, _ := url.Parse(m.Raw)

	ra, err := net.ResolveIPAddr(protocol, uri.Host)
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
	f, e := ioutil.ReadFile("/etc/os-release")
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
		url += m.Version[0] + "/repo/oss/"
	}

	if m.Distro == "fedora" {
		url += m.Version[0] + "/Everything/x86_64/os/"
	}

	return url
}

func main() {
	var geo string
	var ipv6 bool

	flag.StringVar(&geo, "geo", "china", "geo location of the mirror.")
	flag.BoolVar(&ipv6, "ipv6", false, "check ipv6 only.")

	flag.Parse()

	//distro, version := osInfo()
	//raw := "https://mirrors.aliyun.com/opensuse"
	//uri, _ := url.Parse(raw)
	//m := Mirror{"阿里云", raw, uri, distro, []string{version}, geo, ipv6}
	//fmt.Println(m.Ping())

	mirrorlist, _ := readMirrorList("mirrorlist.yaml")
	fmt.Println(mirrorlist)
	mirrorlist.preload(false)
	fmt.Println(mirrorlist)
}
