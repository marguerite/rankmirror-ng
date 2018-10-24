package main

import (
	"github.com/tatsushid/go-fastping"
	"log"
	//"flag"
	"fmt"
	"net"
	"time"
)

func pingMirror(mirror string) time.Duration {
	var t time.Duration
	pinger := fastping.NewPinger()
	ra, err := net.ResolveIPAddr("ip4:icmp", mirror)
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

func averageDuration(mirror string, times int) float64 {
	var durations []time.Duration
	var sum float64
	for i := 0; i < times; i++ {
		durations = append(durations, pingMirror(mirror))
	}
	for _, j := range durations {
		sum += float64(j / time.Millisecond)
	}
	return sum / float64(times)
}

func main() {
	addr := "f1234klll.com"
	fmt.Println(pingMirror(addr))
}
