package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type ServerHost struct {
	URL          *url.URL
	Alive        bool
	mux          sync.RWMutex
	ReverseProxy *httputil.ReverseProxy
}

func (b *ServerHost) SetAlive(alive bool) {
	b.mux.Lock()
	b.Alive = alive
	b.mux.Unlock()
}

func (b *ServerHost) IsAlive() (alive bool) {
	b.mux.RLock()
	alive = b.Alive
	b.mux.RUnlock()
	return
}

type ServerPool struct {
	serverHosts []*ServerHost
	current     uint64
}

func (s *ServerPool) AddServerHost(serverHost *ServerHost) {
	s.serverHosts = append(s.serverHosts, serverHost)
}

func (s *ServerPool) NextIndex() int {
	return int(atomic.AddUint64(&s.current, uint64(1)) % uint64(len(s.serverHosts)))
}

func (s *ServerPool) MarkServerHostStatus(serverHostUrl *url.URL, alive bool) {
	for _, b := range s.serverHosts {
		if b.URL.String() == serverHostUrl.String() {
			b.SetAlive(alive)
			break
		}
	}
}

func (s *ServerPool) GetNextPeer() *ServerHost {
	next := s.NextIndex()
	l := len(s.serverHosts) + next
	for i := next; i < l; i++ {
		idx := i % len(s.serverHosts)
		if s.serverHosts[idx].IsAlive() {
			if i != next {
				atomic.StoreUint64(&s.current, uint64(idx))
			}
			return s.serverHosts[idx]
		}
	}
	return nil
}

func (s *ServerPool) HealthCheck() {
	for _, b := range s.serverHosts {
		status := "up"
		alive := isBackendAlive(b.URL)
		b.SetAlive(alive)
		if !alive {
			status = "down"
		}
		log.Printf("%s [%s]\n", b.URL, status)
	}
}

func isBackendAlive(u *url.URL) bool {
	timeout := 2 * time.Second
	conn, err := net.DialTimeout("tcp", u.Host, timeout)
	if err != nil {
		log.Println("Site unreachable, error: ", err)
		return false
	}
	defer conn.Close()
	return true
}

func healthCheck() {
	t := time.NewTicker(time.Minute * 2)
	for {
		select {
		case <-t.C:
			log.Println("Starting health check...")
			serverPool.HealthCheck()
			log.Println("Health check completed")
		}
	}
}

func dispatch(w http.ResponseWriter, r *http.Request) {

	peer := serverPool.GetNextPeer()
	if peer != nil {
		peer.ReverseProxy.ServeHTTP(w, r)
		return
	}
	http.Error(w, "Service not available", http.StatusServiceUnavailable)
}

func NewProxy(targetHost string) (*httputil.ReverseProxy, error) {
	//serverUrl, err := url.Parse("http://127.0.0.1:8081")
	serverUrl, err := url.Parse(targetHost)
	if err != nil {
		log.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(serverUrl)

	proxy.ErrorHandler = func(writer http.ResponseWriter, request *http.Request, e error) {
		serverPool.MarkServerHostStatus(serverUrl, false)
		dispatch(writer, request.WithContext(nil))
	}
	serverPool.AddServerHost(&ServerHost{
		URL:          serverUrl,
		Alive:        true,
		ReverseProxy: proxy,
	})
	log.Printf("Configured server: %s\n", serverUrl)

	return proxy, nil
}

var serverPool ServerPool

func main() {
	var serverList string
	var port int
	flag.StringVar(&serverList, "host", "", "server list")
	flag.IntVar(&port, "port", 80, "Port")
	flag.Parse()

	if len(serverList) == 0 {
		log.Fatal("serverHost is empty")
	}
	serverUrl := strings.Split(serverList, ",")
	for _, sUrl := range serverUrl {
		_, err := NewProxy(sUrl)
		if err != nil {
			panic(err)
		}
	}

	server := http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: http.HandlerFunc(dispatch),
	}

	// start health checking
	go healthCheck()

	log.Printf("Load Balancer started at :%d\n", port)
	if err := server.ListenAndServe(); err != nil {
		log.Fatal(err)
	}

}
