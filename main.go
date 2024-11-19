package main

import (
	"bytes"
	"compress/gzip"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

type ProxyHandler struct {
	Proxy *httputil.ReverseProxy
}

func NewProxyHandler(destUrl *url.URL) *ProxyHandler {
	ph := ProxyHandler{
		Proxy: httputil.NewSingleHostReverseProxy(destUrl),
	}
	ph.Proxy.Transport = &ph
	return &ph
}

func (t *ProxyHandler) RoundTrip(req *http.Request) (*http.Response, error) {
	sb := strings.Builder{}

	// set this so that it correctly sets it later
	req.Host = ""

	fmt.Fprintf(&sb, "%v %v %v\n\n", req.Method, req.URL.Path, req.Proto)

	for key, val := range req.Header {
		fmt.Fprintf(&sb, "%v: %v\n", key, strings.Join(val, ","))
	}

	if req.Body != nil {
		defer req.Body.Close()
		buf, _ := io.ReadAll(req.Body)
		req_rc := io.NopCloser(bytes.NewBuffer(buf))
		req.Body = req_rc

		sb.WriteString("\n")
		sb.Write(buf)
	}

	resp, err := http.DefaultTransport.RoundTrip(req)

	fmt.Fprintf(&sb, "\n\n%v\n", resp.StatusCode)

	for key, val := range resp.Header {
		fmt.Fprintf(&sb, "%v: %v\n", key, strings.Join(val, ","))
	}

	if resp.Body != nil {
		defer resp.Body.Close()
		buf, _ := io.ReadAll(resp.Body)
		req_rc := io.NopCloser(bytes.NewBuffer(buf))
		resp.Body = req_rc

		if resp.Header.Get("Content-Encoding") == "gzip" {
			reader := bytes.NewReader(buf)
			gzreader, e1 := gzip.NewReader(reader)
			if e1 != nil {
				fmt.Println(e1) // Maybe panic here, depends on your error handling.
			}

			output, e2 := ioutil.ReadAll(gzreader)
			if e2 != nil {
				fmt.Println(e2)
			}
			sb.WriteString("\n<<< GZIP ENCODED >>>\n")
			sb.WriteString(string(output))
		} else {
			sb.WriteString("\n")
			sb.Write(buf)
		}
	}

	log.Printf("\n%v", sb.String())

	return resp, err
}

func (h *ProxyHandler) ProxyRequest(w http.ResponseWriter, r *http.Request) {
	log.Printf("> ProxyRequest, Client: %v, %v %v %v\n", r.RemoteAddr, r.Method, r.URL, r.Proto)
	h.Proxy.ServeHTTP(w, r)
}

func main() {
	var svrAddr string    // proxy server ip
	var svrBaseUrl string // proxy base url
	var destUrlStr string

	flag.StringVar(&svrAddr, "p", "0.0.0.0:9090", "Proxy Server Address")
	flag.StringVar(&destUrlStr, "d", "http://localhost:8080", "destination url")
	flag.StringVar(&svrBaseUrl, "b", "/", "Proxy Server Base Url")

	flag.Parse()
	log.Printf("Start a Server, (HTTP Client) --> [Proxy] %s%s --> [Dest Url] %s\n", svrAddr, svrBaseUrl, destUrlStr)

	// Create a proxy instance with given params
	destUrl, _ := url.Parse(destUrlStr)
	// Create a new ProxyHandler
	proxyHandler := NewProxyHandler(destUrl)

	// Register a handler function
	http.HandleFunc(svrBaseUrl, proxyHandler.ProxyRequest)

	// Start a proxy server
	err := http.ListenAndServe(svrAddr, nil)
	if err != nil {
		panic(err)
	}
}
