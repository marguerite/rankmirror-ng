package main

import (
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fatih/color"
	"github.com/marguerite/diagnose/zypp/repository"
	distro "github.com/marguerite/go-stdlib/os-release"
	"github.com/marguerite/go-stdlib/runtime"
	yaml "gopkg.in/yaml.v2"
)

var (
	client = &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
		},
		Timeout: 30 * time.Second,
	}
)

// Mirrors the mirror list
type Mirrors map[string]string

func NewMirrors(b []byte) (Mirrors, error) {
	var m Mirrors
	err := yaml.Unmarshal(b, &m)
	return m, err
}

func mirrorsPath() (string, error) {
	for _, v := range []string{filepath.Join("/home", runtime.LogName(), ".config", "rankmirror-ng", "mirrors.yaml"), "/usr/share/rankmirror-ng/mirrors.yaml", "mirrors.yaml"} {
		if _, err := os.Stat(v); os.IsNotExist(err) {
			continue
		} else {
			return v, nil
		}
	}
	return "", errors.New("No mirrors.yaml available")
}

func repo() string {
	version := strconv.FormatFloat(distro.Version(), 'f', -1, 64)
	if len(version) > 4 {
		return "tumbleweed"
	}
	return "distribution/leap/" + version
}

func filterByVersion(mirrors Mirrors) []string {
	arr := make([]string, 0, len(mirrors))

	for _, v := range mirrors {
		if v[len(v)-1] != '/' {
			v += "/"
		}
		v1 := v + repo()
		resp, err := client.Get(v1)
		if err != nil || resp.StatusCode != 200 {
			continue
		}
		defer resp.Body.Close()
		arr = append(arr, v)
	}

	return arr
}

func createTmpfs() string {
	dir, err := ioutil.TempDir("/tmp", "rankmirror-ng-*")
	if err != nil {
		panic("can't create tmpfs")
	}
	return dir
}

type speeds []speed

type speed struct {
	u string
	s float64
}

func (s speeds) Len() int {
	return len(s)
}

func (s speeds) Swap(i, j int) {
	s[i], s[j] = s[j], s[i]
}

func (s speeds) Less(i, j int) bool {
	return s[i].s < s[j].s
}

func speedTest(mirrors Mirrors) speeds {
	arr := filterByVersion(mirrors)

	var wg sync.WaitGroup
	var mutex sync.Mutex
	results := make(speeds, 0, len(arr))

	for _, v := range arr {
		wg.Add(1)
		go func(mirror string) {
			defer wg.Done()
			u := mirror + repo() + "/repo/oss"
			dir := createTmpfs()
			defer os.RemoveAll(dir)
			_, err := exec.Command("/usr/bin/zypper", "--root="+dir, "--no-gpg-checks", "ar", "-c", u, strings.Replace(distro.Name(), " ", "_", -1)).Output()
			if err != nil {
				mutex.Lock()
				results = append(results, speed{mirror, 0})
				mutex.Unlock()
				return
			}

			c1 := make(chan string, 1)

			go func() {
				_, err := exec.Command("/usr/bin/zypper", "--root="+dir, "--no-gpg-checks", "refresh").Output()
				if err != nil {
					c1 <- err.Error()
				} else {
					c1 <- "nil"
				}
			}()

			select {
			case res := <-c1:
				if res != "nil" {
					mutex.Lock()
					results = append(results, speed{mirror, 0})
					mutex.Unlock()
					return
				}
			case <-time.After(5 * time.Minute):
				mutex.Lock()
				results = append(results, speed{mirror, 0})
				mutex.Unlock()
				return
			}

			t := time.Now()

			c2 := make(chan string, 1)

			go func() {
				_, err = exec.Command("/usr/bin/zypper", "--root="+dir, "--no-gpg-checks", "install", "--no-recommends", "--download-only", "--no-confirm", "glibc").Output()
				if err != nil {
					c2 <- err.Error()
				} else {
					c2 <- "nil"
				}
			}()

			select {
			case res := <-c2:
				if res != "nil" {
					mutex.Lock()
					results = append(results, speed{mirror, 0})
					mutex.Unlock()
					return
				}
			case <-time.After(5 * time.Minute):
				mutex.Lock()
				results = append(results, speed{mirror, 0})
				mutex.Unlock()
				return
			}

			d := time.Since(t)
			mutex.Lock()
			results = append(results, speed{mirror, d.Seconds()})
			mutex.Unlock()
		}(v)
	}

	wg.Wait()

	return results
}

func replaceMirror(str, str1 string) string {
	u, _ := url.Parse(str)
	u1, _ := url.Parse(str1)
	d := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(distro.Name(), "openSUSE")))
	if strings.Contains(u.Path, d) {
		if len(u.Path) > 0 {
			if u1.Path[len(u1.Path)-1] == '/' {
				u1.Path = u1.Path[:len(u1.Path)-1]
			}
			arr := strings.Split(u.Path, d)
			u1.Path = u1.Path + "/" + d + strings.Join(arr[1:], "")
		}
		return u1.String()
	}
	return str
}

func main() {
	p, err := mirrorsPath()
	if err != nil {
		panic(err)
	}

	b, err := ioutil.ReadFile(p)
	if err != nil {
		panic("failed to read mirrors.yaml")
	}

	mirrors, err := NewMirrors(b)
	if err != nil {
		fmt.Printf("failed to unmarshal %s\n", p)
		os.Exit(1)
	}

	s := speedTest(mirrors)

	sort.Sort(s)

	var zero speeds
	var best speed
	var j int

	color.Set(color.FgGreen, color.Bold)
	defer color.Unset()

	for i := 0; i < len(s); i++ {
		if s[i].s == 0 {
			zero = append(zero, s[i])
			continue
		}
		if j == 0 {
			best = s[i]
		}
		j++
		fmt.Printf("%s:\t%0.2fs\n", s[i].u, s[i].s)
	}

	color.Set(color.FgRed, color.Bold)

	for i := 0; i < len(zero); i++ {
		fmt.Printf("%s:\t%0.2fs\n", s[i].u, s[i].s)
	}

	u, _ := user.Current()
	if u.Username == "root" || u.Uid == "0" {
		repositories := repository.NewRepositories()
		for _, v := range repositories {
			replace := replaceMirror(v.BaseURL, best.u)
			if replace != v.BaseURL {
				v.BaseURL = replace
				v.Marshal()
				fmt.Printf("set mirror for %s\n", v.File)
			}
		}

		_, err := exec.Command("/usr/bin/zypper", "--no-gpg-checks", "refresh").Output()
		if err != nil {
			panic("failed to refresh repositories")
		}
	}
}
