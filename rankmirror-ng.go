package main

import (
	"flag"
	"fmt"
	"io/ioutil"
	"math"
	"net"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/cavaliercoder/grab"
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

func readConfig(file string) (Config, error) {
	config := Config{}

	b, err := ioutil.ReadFile(file)
	if err != nil {
		return config, err
	}

	err = yaml.Unmarshal(b, &config)
	if err != nil {
		return config, err
	}

	return config, nil
}

type MirrorList []Mirror

func (mirrorlist *MirrorList) preload(config Config, force bool) {
	for i := range *mirrorlist {
		err := (*mirrorlist)[i].preload(config, force)
		if err != nil {
			fmt.Println(err)
			continue
		}
	}
}

type Mirror struct {
	Name          string        `yaml:"name"`
	Raw           string        `yaml:"raw"`
	Distro        string        `yaml: "distro"`
	Version       []string      `yaml: "version"`
	Country       string        `yaml: "country"`
	Latitude      float64       `yaml: "latitude"`
	Longitude     float64       `yaml: "longitude"`
	Distance      float64       `yaml: "distance"`
	PingSpeed     time.Duration `yaml: "ping"`
	DownloadSpeed time.Duration `yaml: "download"`
	Weight        float64       `yaml: "weight"`
	IPv6          bool          `yaml: "ipv6"`
}

func (m *Mirror) preload(config Config, force bool) error {
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

	if len(m.Version) == 0 || force {
		var err error
		m.Version, err = probeDistroVersions(m.Distro, m.Raw)
		if err != nil {
			return fmt.Errorf("Unsupported OS: %s, %v", m.Distro, *m)
		}
	}

	if len(m.Country) == 0 || m.Latitude == 0 || m.Longitude == 0 || force {
		uri, _ := url.Parse(m.Raw)
		ra, err := net.ResolveIPAddr("ip4", uri.Host)
		if err != nil {
			return fmt.Errorf("geoLocateIP failed: %v, %v", err, *m)
		}
		m.Country, m.Latitude, m.Longitude, err = geoLocateIP(ra.String())
		if err != nil {
			return fmt.Errorf("geoLocateIP failed: %v, %v", err, *m)
		}
	}

	if m.Distance == 0 || force {
		m.Distance = calGeoDistance(m.Latitude, m.Longitude, config.Latitude, config.Longitude)
	}

	if m.PingSpeed == 0 || force {
		ping, _ := m.Ping()
		if ping == 0 {
			m.PingSpeed, _ = time.ParseDuration("999h")
		} else {
			m.PingSpeed = ping
		}
	}

	if m.DownloadSpeed == 0 || force {
		download, _ := m.TryDownload()
		if download == 0 {
			m.DownloadSpeed, _ = time.ParseDuration("999h")
		} else {
			m.DownloadSpeed = download
		}
	}

	if m.Weight == 0 || force {
		m.Weight = m.Distance*config.DistanceWeight + m.PingSpeed.Seconds()*config.PingWeight + m.DownloadSpeed.Seconds()*config.DownloadWeight
	}

	return nil
}

func (m Mirror) Repo(version string) (string, error) {
	suffix, err := genRepoSuffix(m.Distro)
	if err != nil {
		return "", err
	}
	uri, _ := url.Parse(m.Raw)
	uri.Path = path.Join(uri.Path, version, suffix)
	return uri.String(), nil
}

func (m Mirror) Ping() (time.Duration, error) {
	var t time.Duration
	protocol := "ip4:icmp"
	pinger := fastping.NewPinger()
	if m.IPv6 {
		protocol = "ip6:icmp"
	}

	uri, _ := url.Parse(m.Raw)

	ra, err := net.ResolveIPAddr(protocol, uri.Host)
	if err != nil {
		return t, err
	}
	pinger.AddIPAddr(ra)
	pinger.OnRecv = func(addr *net.IPAddr, rtt time.Duration) {
		t = rtt
	}
	pinger.OnIdle = func() {}
	err = pinger.Run()
	if err != nil {
		return t, err
	}
	return t, nil
}

func (m Mirror) TryDownload() (time.Duration, error) {
	var t time.Duration
	repo, _ := m.Repo(m.Version[0])
	fmt.Println(repo)
	resp, err := http.Get(repo)
	if err != nil {
		fmt.Println(err)
		return t, err
	}
	defer resp.Body.Close()

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return t, nil
	}

	file, _ := doc.Find("table#list tbody tr:nth-child(2) td a").Attr("href")
	uri, _ := url.Parse(repo)
	uri.Path = path.Join(uri.Path, file)

	t1 := time.Now()

	done := make(chan bool)
	var err1 error

	go func() {
		resp, err := grab.Get(path.Join("/tmp", path.Base(uri.String())), uri.String())
		if err != nil {
			err1 = err
			done <- true
		}
		if resp.IsComplete() {
			done <- true
		}
	}()

	<-done

	if err1 != nil {
		return t, err1
	}

	t = time.Now().Sub(t1)

	return t, nil
}

func calGeoDistance(lat1, lng1, lat2, lng2 float64) float64 {
	radlat1 := float64(math.Pi * lat1 / 180)
	radlat2 := float64(math.Pi * lat2 / 180)

	theta := float64(lng1 - lng2)
	radtheta := float64(math.Pi * theta / 180)

	dist := math.Sin(radlat1)*math.Sin(radlat2) + math.Cos(radlat1)*math.Cos(radlat2)*math.Cos(radtheta)

	if dist > 1 {
		dist = 1
	}

	dist = math.Acos(dist)
	dist = dist * 180 / math.Pi
	dist = dist * 60 * 1.1515 * 1.609344

	return dist
}

func genRepoPaths(distro string) ([]string, error) {
	paths := []string{}
	switch distro {
	case "opensuse":
		paths = []string{"distribution/leap/15.1", "distribution/leap/15.2", "distribution/leap/15.3", "tumbleweed"}
	default:
		return paths, fmt.Errorf("Unhandle Linux distribution %s", distro)
	}
	return paths, nil
}

func genRepoSuffix(distro string) (string, error) {
	suffix := ""
	switch distro {
	case "opensuse":
		suffix = "repo/oss/repodata"
	default:
		return suffix, fmt.Errorf("Unhandle Linux distribution %s", distro)
	}
	return suffix, nil
}

func probeDistroVersions(distro, raw string) ([]string, error) {
	versions := []string{}
	paths, err := genRepoPaths(distro)
	if err != nil {
		return versions, err
	}
	suffix, _ := genRepoSuffix(distro)

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

	return versions, nil
}

func geoLocateIP(raw string) (string, float64, float64, error) {
	db, err := geoip2.Open("GeoLite2-City.mmdb")
	if err != nil {
		return "", 0, 0, err
	}
	defer db.Close()

	ip := net.ParseIP(raw)
	record, err := db.City(ip)
	if err != nil {
		return "", 0, 0, err
	}
	return record.Country.Names["en"], record.Location.Latitude, record.Location.Longitude, nil
}

type Config struct {
	OS             string
	Version        string
	IP             string
	Latitude       float64
	Longitude      float64
	DistanceWeight float64
	RouteWeight    float64
	PingWeight     float64
	DownloadWeight float64
}

func (c *Config) preload(force bool) {
	os, version, _ := osInfo()
	ip, _ := probeIP()
	_, la, lo, _ := geoLocateIP(ip)

	if len(c.OS) == 0 {
		c.OS = os
	}

	if len(c.Version) == 0 || c.Version != version {
		c.Version = version
	}

	if c.Latitude == 0 || c.Longitude == 0 || c.IP != ip || force {
		c.Latitude = la
		c.Longitude = lo
	}

	if len(c.IP) == 0 || c.IP != ip || force {
		c.IP = ip
	}

	if c.DistanceWeight == 0 {
		c.DistanceWeight = 0.25
	}

	if c.RouteWeight == 0 {
		c.RouteWeight = 0.25
	}

	if c.PingWeight == 0 {
		c.PingWeight = 0.25
	}

	if c.DownloadWeight == 0 {
		c.DownloadWeight = 0.25
	}
}

func osInfo() (string, string, error) {
	f, err := ioutil.ReadFile("/etc/os-release")
	if err != nil {
		return "", "", err
	}

	id_r := regexp.MustCompile(`(?m)^ID=("opensuse-)?([^"]+)(")?$`)
	ver_r := regexp.MustCompile(`(?m)^VERSION_ID=(")?([^"]+)(")?$`)

	id := id_r.FindStringSubmatch(string(f))[2]
	version := "0.0"

	if ver_r.MatchString(string(f)) {
		version = ver_r.FindStringSubmatch(string(f))[2]
	}

	return id, version, nil
}

func probeIP() (string, error) {
	resp, err := http.Get("https://myexternalip.com/raw")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(b), nil
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
	config, _ := readConfig("config.yaml")
	config.preload(false)
	fmt.Println(config)
	mirrorlist.preload(config, false)
	fmt.Println(mirrorlist)
}
