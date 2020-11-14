package main

import (
	"crypto/rand"
	"flag"
	"fmt"
	"io/ioutil"
	"math"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/user"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/aeden/traceroute"
	"github.com/cavaliercoder/grab"
	"github.com/marguerite/diagnose/zypp/repository"
	"github.com/marguerite/go-stdlib/fileutils"
	"github.com/marguerite/go-stdlib/httputils"
	osrelease "github.com/marguerite/go-stdlib/os-release"
	"github.com/marguerite/go-stdlib/runtime"
	"github.com/marguerite/go-stdlib/slice"
	"github.com/olekukonko/tablewriter"
	geoip2 "github.com/oschwald/geoip2-golang"
	"github.com/tatsushid/go-fastping"
	yaml "gopkg.in/yaml.v2"
)

var (
	mirrorlistPath = filepath.Join("/home", runtime.LogName(), ".config/rankmirror-ng/mirrorlist.yaml")
	configPath     = filepath.Join("/home", runtime.LogName(), ".config/rankmirror-ng/config.yaml")
	geoDbPath      = filepath.Join("/home", runtime.LogName(), ".config/rankmirror-ng/GeoLite2-City.mmdb")
)

func readMirrorList() (m MirrorList, err error) {
	_, err = os.Stat(mirrorlistPath)
	if os.IsNotExist(err) {
		fileutils.Copy("mirrorlist.yaml", mirrorlistPath)
	}
	b, err := ioutil.ReadFile(mirrorlistPath)
	if err != nil {
		return m, err
	}

	err = yaml.Unmarshal(b, &m)

	return m, err
}

func readConfig() (c Config, err error) {
	_, err = os.Stat(configPath)
	if os.IsNotExist(err) {
		fileutils.Copy("config.yaml", configPath)
	}
	b, err := ioutil.ReadFile(configPath)
	if err != nil {
		return c, err
	}

	err = yaml.Unmarshal(b, &c)

	return c, err
}

func readGeoDB() (db []byte, err error) {
	_, err = os.Stat(geoDbPath)
	if os.IsNotExist(err) {
		return db, fmt.Errorf("Sorry, you have no GeoLite2-City.mmdb available in %s, you should download one from maxmind, see: https://blog.maxmind.com/2019/12/18/significant-changes-to-accessing-and-using-geolite2-databases/", geoDbPath)
	}

	db, err = ioutil.ReadFile(geoDbPath)
	if err != nil {
		return db, err
	}
	return db, nil
}

type MirrorList []Mirror

func (m *MirrorList) init(c Config, force bool, geoDB []byte) {
	wg := sync.WaitGroup{}
	wg.Add(len(*m))

	for i := range *m {
		go func(j int) {
			defer wg.Done()
			err := (*m)[j].init(c, force, geoDB)
			if err != nil {
				fmt.Println(err)
			}
		}(i)
	}

	wg.Wait()
}

func (m MirrorList) save() {
	b, err := yaml.Marshal(m)
	if err != nil {
		fmt.Println(err)
		return
	}
	err = ioutil.WriteFile(mirrorlistPath, b, 0644)
	if err != nil {
		fmt.Println(err)
	}
}

func (m MirrorList) Len() int {
	return len(m)
}

func (m MirrorList) Less(i, j int) bool {
	return m[i].Weight < m[j].Weight
}

func (m MirrorList) Swap(i, j int) {
	m[i], m[j] = m[j], m[j]
}

func (m MirrorList) Rank(c Config) (result [][]string) {
	sort.Sort(m)
	for _, v := range m {
		if v.Distro != c.OS {
			continue
		}
		if ok, err := slice.Contains(v.Version, c.Variant); !ok || err != nil {
			continue
		}
		w, _ := strconv.ParseFloat(fmt.Sprintf("%.2f", v.Weight), 64)
		result = append(result, []string{v.Name, v.Country,
			strconv.FormatFloat(w, 'f', -1, 64),
			fmt.Sprintf("%.2f", v.Distance) + "km",
			v.RouteTime.String() + " (" + strconv.FormatFloat(v.RouteLevel, 'f', -1, 64) + " levels)",
			v.PingSpeed.String(), fmt.Sprintf("%.2f", v.DownloadSpeed) + " KB/S", v.Raw})
	}
	return result
}

func (m MirrorList) FindByName(name string) (m1 Mirror) {
	for _, v := range m {
		if v.Name == name {
			m1 = v
		}
	}
	return m1
}

type Mirror struct {
	Name          string        `yaml:"name"`
	IP            string        `yaml:"ip"`
	Raw           string        `yaml:"raw"`
	Distro        string        `yaml: "distro"`
	Version       []string      `yaml: "version"`
	Country       string        `yaml: "country"`
	Latitude      float64       `yaml: "latitude"`
	Longitude     float64       `yaml: "longitude"`
	Distance      float64       `yaml: "distance"`
	RouteLevel    float64       `yaml: "routelevel"`
	RouteTime     time.Duration `yaml: "routetime"`
	PingSpeed     time.Duration `yaml: "ping"`
	DownloadSpeed float64       `yaml: "download"`
	Weight        float64       `yaml: "weight"`
	IPv6          bool          `yaml: "ipv6"`
}

func (m *Mirror) init(c Config, force bool, geoDB []byte) error {
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
		re := regexp.MustCompile(`\/\/\w+\.([^\.]+)`)
		m.Name = strings.Title(re.FindStringSubmatch(m.Raw)[1])
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
		m.Country, m.Latitude, m.Longitude, err = geoLocateIP(m.IP, geoDB)
		if err != nil {
			return fmt.Errorf("geoLocateIP failed: %v, %v", err, *m)
		}
	}

	if m.Distance == 0 || force {
		m.Distance = calGeoDistance(m.Latitude, m.Longitude, c.Latitude, c.Longitude)
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

		m.DownloadSpeed = download
		if download == 0 {
			m.DownloadSpeed = 0.001
		}
	}

	if m.Weight == 0 || force {
		m.Weight = m.Distance*c.DistanceWeight + m.RouteLevel*c.RouteLevelWeight + m.RouteTime.Seconds()*c.RouteTimeWeight + m.PingSpeed.Seconds()*c.PingWeight + c.DownloadWeight*1/m.DownloadSpeed
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
	defaultDuration, _ := time.ParseDuration("1s")
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
				t += defaultDuration
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

//TryDownload randomly download a repo metadata file and return the speed in kilobytes/second
func (m Mirror) TryDownload() (float64, error) {
	repo, _ := m.Repo(m.Version[0])
	resp, err := http.Get(repo)

	fmt.Printf("Randomly pick a metadata file from %s\n", repo)

	if err != nil {
		fmt.Println(err)
		return 0, err
	}
	defer resp.Body.Close()

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return 0, nil
	}

	var links []string
	doc.Find("a").Each(
		func(i int, s *goquery.Selection) {
			link, _ := s.Attr("href")
			if strings.HasSuffix(link, "xml.gz") {
				links = append(links, link)
			}
		})

	if len(links) == 0 {
		return 0, fmt.Errorf("No repodata found, uri %s", repo)
	}

	// randomly download a repodata xml
	random, _ := rand.Int(rand.Reader, big.NewInt(int64(len(links))))

	uri, _ := url.Parse(repo)
	uri.Path = path.Join(uri.Path, links[random.Int64()])

	file := path.Join("/tmp", strconv.FormatInt(time.Now().UnixNano()/int64(time.Nanosecond), 10)+"-"+path.Base(uri.String()))
	fmt.Printf("Downloading %s to %s\n", uri.String(), file)

	client := grab.NewClient()
	client.HTTPClient.Timeout = 30 * time.Second

	req, err := grab.NewRequest(file, uri.String())
	if err != nil {
		fmt.Println(err)
		return 0, err
	}
	resp1 := client.Do(req)

	if resp1.Err() != nil {
		fmt.Printf("Download Error: %v\n", resp1.Err())
		return 0, resp1.Err()
	}

	if resp1.IsComplete() {
		kilobytesPerSecond := float64(resp1.BytesComplete()/1024.00) / resp1.Duration().Seconds()
		fmt.Printf("Download Completed with speed %.2f kilobytes/second\n", kilobytesPerSecond)
		os.RemoveAll(file)
		return kilobytesPerSecond, nil
	}

	os.RemoveAll(file)
	return 0, nil
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
	var paths []string
	switch distro {
	case "opensuse":
		paths = []string{"distribution/leap/15.1", "distribution/leap/15.2", "distribution/leap/15.3", "tumbleweed"}
	default:
		return paths, fmt.Errorf("Unhandle Linux distribution %s", distro)
	}
	return paths, nil
}

func genRepoSuffix(distro string) (string, error) {
	var suffix string
	switch distro {
	case "opensuse":
		suffix = "repo/oss/repodata"
	default:
		return suffix, fmt.Errorf("Unhandle Linux distribution %s", distro)
	}
	return suffix, nil
}

func probeDistroVersions(distro, raw string) ([]string, error) {
	var versions []string
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

func geoLocateIP(addr string, geoDB []byte) (country string, latitude float64, longitude float64, err error) {
	db, err := geoip2.FromBytes(geoDB)
	if err != nil {
		return country, latitude, longitude, err
	}
	defer db.Close()

	ip := net.ParseIP(addr)
	record, err := db.City(ip)
	if err != nil {
		return country, latitude, longitude, err
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

func (c *Config) init(ip string, geoDB []byte, force bool) {
	variant, version := osInfo()

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
		_, la, lo, _ := geoLocateIP(ip, geoDB)
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
	err = ioutil.WriteFile(configPath, b, 0644)
	if err != nil {
		fmt.Println(err)
	}
}

func osInfo() (string, string) {
	version := osrelease.Version()
	name := strings.ToLower(osrelease.Name())
	if strings.Contains(name, "opensuse") {
		name = strings.TrimPrefix(name, "opensuse-")
	}
	return name, strconv.Itoa(version)
}

func main() {
	var list, update, set bool
	var mirror string

	flag.StringVar(&mirror, "mirror", "", "the mirror to use via its name")
	flag.BoolVar(&set, "set", false, "whether to set a mirror")
	flag.BoolVar(&list, "list", false, "list the mirrors")
	flag.BoolVar(&update, "update", false, "update the mirrors")
	flag.Parse()

	config, _ := readConfig()
	ip, _ := httputils.LocalIPAddress()
	var db []byte

	if config.IP != ip || update {
		db1, err := readGeoDB()
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
		db = db1
	}

	config.init(ip, db, update)
	config.save()

	mirrorlist, _ := readMirrorList()

	mirrorlist.init(config, update, db)
	mirrorlist.save()
	result := mirrorlist.Rank(config)

	if list {
		table := tablewriter.NewWriter(os.Stdout)
		table.SetHeader([]string{"Name", "Location", "Weight", "Distance", "Route", "Ping", "Download", "Mirror URL"})
		table.SetBorder(false)
		table.AppendBulk(result)
		table.Render()
	}

	if set {
		u, _ := user.Current()
		if u.Username != "root" || u.Uid != "0" {
			panic("must be root to run this program")
		}
		selected := result[0][len(result[0])-1]
		if len(mirror) > 0 {
			selected = mirrorlist.FindByName(mirror).Raw
		}
		repositories := repository.NewRepositories()
		for _, v := range repositories {
			if strings.Contains(v.BaseURL, config.Variant) {
				replace := strings.Split(v.BaseURL, config.Variant)[0]
				v.BaseURL = strings.Replace(v.BaseURL, replace, selected, 1)
				v.Marshal()
				fmt.Printf("set mirror for %s\n", v.File)
			}
		}
	}
}
