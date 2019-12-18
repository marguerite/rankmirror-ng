package main

import (
	"flag"
	"fmt"
	"io/ioutil"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/aeden/traceroute"
	"github.com/cavaliercoder/grab"
	"github.com/marguerite/util/slice"
	"github.com/olekukonko/tablewriter"
	geoip2 "github.com/oschwald/geoip2-golang"
	"github.com/tatsushid/go-fastping"
	yaml "gopkg.in/yaml.v2"
)

func readMirrorList() (MirrorList, error) {
	mirrorlist := MirrorList{}

	b, err := ioutil.ReadFile("mirrorlist.yaml")
	if err != nil {
		return mirrorlist, err
	}

	err = yaml.Unmarshal(b, &mirrorlist)
	if err != nil {
		return mirrorlist, err
	}

	return mirrorlist, nil
}

func readConfig() (Config, error) {
	config := Config{}

	b, err := ioutil.ReadFile("config.yaml")
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

func (mirrorlist MirrorList) save() {
	b, err := yaml.Marshal(mirrorlist)
	if err != nil {
		fmt.Println(err)
		return
	}
	err = ioutil.WriteFile("mirrorlist.yaml", b, 0644)
	if err != nil {
		fmt.Println(err)
	}
}

func (mirrorlist MirrorList) Len() int {
	return len(mirrorlist)
}

func (mirrorlist MirrorList) Less(i, j int) bool {
	return mirrorlist[i].Weight < mirrorlist[j].Weight
}

func (mirrorlist MirrorList) Swap(i, j int) {
	mirrorlist[i], mirrorlist[j] = mirrorlist[j], mirrorlist[j]
}

func (mirrorlist MirrorList) Rank(config Config) {
	sort.Sort(mirrorlist)
	result := [][]string{}
	for _, m := range mirrorlist {
		if m.Distro != config.OS {
			continue
		}
		if ok, err := slice.Contains(m.Version, config.Variant); !ok || err != nil {
			continue
		}
		w, _ := strconv.ParseFloat(fmt.Sprintf("%.2f", m.Weight), 64)
		result = append(result, []string{m.Name, m.Country,
			strconv.FormatFloat(w, 'f', -1, 64),
			fmt.Sprintf("%.2f", m.Distance) + "km",
			m.RouteTime.String() + " (" + strconv.FormatFloat(m.RouteLevel, 'f', -1, 64) + " levels)",
			m.PingSpeed.String(), m.DownloadSpeed.String(), m.Raw})
	}
	table := tablewriter.NewWriter(os.Stdout)
	table.SetHeader([]string{"Name", "Location", "Weight", "Distance", "Route", "Ping", "Download", "Mirror URL"})
	table.SetBorder(false)
	table.AppendBulk(result)
	table.Render()
}

type Mirror struct {
	Name                string        `yaml:"name"`
	IP                  string        `yaml:"ip"`
	Raw                 string        `yaml:"raw"`
	Distro              string        `yaml: "distro"`
	Version             []string      `yaml: "version"`
	Country             string        `yaml: "country"`
	Latitude            float64       `yaml: "latitude"`
	Longitude           float64       `yaml: "longitude"`
	Distance            float64       `yaml: "distance"`
	RouteLevel          float64       `yaml: "routelevel"`
	RouteTime           time.Duration `yaml: "routetime"`
	PingSpeed           time.Duration `yaml: "ping"`
	DownloadSpeed       time.Duration `yaml: "download"`
	DownloadCssSelector string        `yaml: "downloadcssselector"`
	Weight              float64       `yaml: "weight"`
	IPv6                bool          `yaml: "ipv6"`
}

func (m *Mirror) preload(config Config, force bool) error {
	// Raw is a must field
	if len(m.Raw) == 0 {
		return fmt.Errorf("raw field is required: %v", *m)
	}

	if len(m.IP) == 0 {
		uri, _ := url.Parse(m.Raw)
		ip, err := net.ResolveIPAddr("ip", uri.Host)
		if err != nil {
			return fmt.Errorf("Failed to resolve %s", uri.Host)
		}
		m.IP = ip.String()
	}

	if len(m.Name) == 0 {
		// https://mirrors.tuna.tsinghua.edu.cn/opensuse
		re := regexp.MustCompile(`mirror(s)?\.(\w+)`)
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
		var err error
		m.Country, m.Latitude, m.Longitude, err = geoLocateIP(m.IP)
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

	if m.RouteLevel == 0 || force {
		fmt.Printf("Traceroute %s\n", m.Raw)
		level, speed, err := m.TraceRoute()
		if err != nil {
			return err
		}
		m.RouteLevel = level
		if speed == 0 {
			speed1, _ := time.ParseDuration("999h")
			speed = speed1
		}
		m.RouteTime = speed
	}

	if m.DownloadSpeed == 0 || force {
		download, err := m.TryDownload()
		if err != nil {
			return err
		}
		if download == 0 {
			m.DownloadSpeed, _ = time.ParseDuration("999h")
		} else {
			m.DownloadSpeed = download
		}
	}

	if m.Weight == 0 || force {
		m.Weight = m.Distance*config.DistanceWeight + m.RouteLevel*config.RouteLevelWeight + m.RouteTime.Seconds()*config.RouteTimeWeight + m.PingSpeed.Seconds()*config.PingWeight + m.DownloadSpeed.Seconds()*config.DownloadWeight
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
	ra, err := net.ResolveIPAddr(protocol, m.IP)
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

func (m Mirror) TraceRoute() (float64, time.Duration, error) {
	var t time.Duration
	opts := traceroute.TracerouteOptions{}
	opts.SetTimeoutMs(10)
	opts.SetRetries(0)
	opts.SetMaxHops(20)
	c := make(chan traceroute.TracerouteHop, 0)
	level := 0.0
	go func() {
		for {
			hop, ok := <-c
			if !ok {
				return
			}
			addr := fmt.Sprintf("%v.%v.%v.%v", hop.Address[0], hop.Address[1], hop.Address[2], hop.Address[3])
			hostOrAddr := addr
			if hop.Host != "" {
				hostOrAddr = hop.Host
			}
			if hop.Success {
				fmt.Printf("%-3d %v (%v) %v\n", hop.TTL, hostOrAddr, addr, hop.ElapsedTime)
				t += hop.ElapsedTime
			} else {
				fmt.Printf("%-3d *\n", hop.TTL)
			}
			level += 1
		}
	}()

	_, err := traceroute.Traceroute(m.IP, &opts, c)
	if err != nil {
		return 999, t, err
	}
	return level, t, nil
}

func (m Mirror) TryDownload() (time.Duration, error) {
	var t time.Duration
	repo, _ := m.Repo(m.Version[0])
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
	file, _ := doc.Find(m.DownloadCssSelector).Attr("href")
	if len(file) == 0 {
		return t, fmt.Errorf("No matched CSS Selector found: %s, uri %s", m.DownloadCssSelector, repo)
	}
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
	OS               string  `yaml: "os"`
	Variant          string  `yaml: "variant"`
	Version          string  `yaml: "version"`
	IP               string  `yaml: "ip"`
	Latitude         float64 `yaml: "latitude"`
	Longitude        float64 `yaml: "longtitude"`
	DistanceWeight   float64 `yaml: "physicaldistanceweight"`
	RouteLevelWeight float64 `yaml: "routelevelweight"`
	RouteTimeWeight  float64 `yaml: "routetimeweight"`
	PingWeight       float64 `yaml: "pingspeedweight"`
	DownloadWeight   float64 `yaml: "downloadspeedweight"`
}

func (c *Config) preload(force bool) {
	variant, version, _ := osInfo()
	ip, _ := probeIP()
	_, la, lo, _ := geoLocateIP(ip)

	if len(c.OS) == 0 {
		c.OS = variant
	}

	if len(c.Variant) == 0 {
		c.Variant = variant
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
		c.DistanceWeight = 0.2
	}

	if c.RouteLevelWeight == 0 {
		c.RouteLevelWeight = 0.2
	}

	if c.RouteTimeWeight == 0 {
		c.RouteTimeWeight = 0.2
	}

	if c.PingWeight == 0 {
		c.PingWeight = 0.2
	}

	if c.DownloadWeight == 0 {
		c.DownloadWeight = 0.2
	}
}

func (c Config) save() {
	b, err := yaml.Marshal(c)
	if err != nil {
		fmt.Println(err)
		return
	}
	err = ioutil.WriteFile("config.yaml", b, 0644)
	if err != nil {
		fmt.Println(err)
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
	var list bool
	var update bool
	var set string

	flag.StringVar(&set, "set", "", "the mirror to use via its name")
	flag.BoolVar(&list, "list", false, "list the mirrors")
	flag.BoolVar(&update, "update", false, "update the mirrors")
	flag.Parse()

	config, _ := readConfig()
	config.preload(update)
	config.save()

	mirrorlist, _ := readMirrorList()

	mirrorlist.preload(config, update)
	mirrorlist.save()

	if list {
		mirrorlist.Rank(config)
	}

	if len(set) > 0 {

	}
}
