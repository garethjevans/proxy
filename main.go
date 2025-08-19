package main

import (
	"bytes"
	"compress/gzip"
	"crypto/tls"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

type ProxyHandler struct {
	Proxy     *httputil.ReverseProxy
	transport *http.Transport
}

// forceHTTP1Transport creates a new HTTP transport that forces HTTP/1.1
func forceHTTP1Transport() *http.Transport {
	return &http.Transport{
		ForceAttemptHTTP2:  false,
		DisableCompression: false,
		DisableKeepAlives:  false,
		// Copy default transport settings
		Proxy:                 http.ProxyFromEnvironment,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		// Explicitly configure TLS
		TLSNextProto: make(map[string]func(authority string, c *tls.Conn) http.RoundTripper), // Disable HTTP/2 support
	}
}

func NewProxyHandler(destUrl *url.URL) *ProxyHandler {
	transport := forceHTTP1Transport()

	// Create the reverse proxy
	proxy := httputil.NewSingleHostReverseProxy(destUrl)

	// Create a custom director that removes h2c upgrade headers
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		// Call the original director
		originalDirector(req)

		// Remove any h2c upgrade headers
		req.Header.Del("Upgrade")
		req.Header.Del("Connection")

		req.Header.Del("X-Amz-Content-Sha256")

		// Amz-Sdk-Invocation-Id:7315a103-219b-b3d5-a4d5-88ae5085e572
		// Amz-Sdk-Request:attempt=1; max=4
		// Authorization:AWS4-HMAC-SHA256 Credential=AKIAZOSE4Z5CBG7MCDV2/20250819/us-east-1/bedrock/aws4_request, SignedHeaders=amz-sdk-invocation-id;amz-sdk-request;content-length;content-type;host;x-amz-content-sha256;x-amz-date, Signature=35669782c2bee09be569ad95a97ae5bde5d09f105494f02dbe502322ed2a1a07
		// Content-Length:795
		// Content-Type:application/json
		// User-Agent:aws-sdk-java/2.31.65 md/io#async md/http#NettyNio ua/2.1 os/Mac_OS_X#15.6 lang/java#21.0.4 md/OpenJDK_64-Bit_Server_VM#21.0.4+7-LTS md/vendor#Eclipse_Adoptium md/en_GB md/kotlin/1.9.25-release-852 cfg/auth-source#stat m/D
		// X-Amz-Content-Sha256:4c9bfbe5b5d59a159eebbda2df2093b3c490fc0c88376ef3267da717e225ce14 X-Amz-Date:20250819T131606Z X-Forwarded-For:127.0.0.1

		// Ensure HTTP/1.1 is used
		req.Proto = "HTTP/1.1"
		req.ProtoMajor = 1
		req.ProtoMinor = 1
	}

	ph := ProxyHandler{
		Proxy:     proxy,
		transport: transport,
	}
	ph.Proxy.Transport = &ph
	return &ph
}

func (t *ProxyHandler) RoundTrip(req *http.Request) (*http.Response, error) {
	startTime := time.Now()
	requestLogger := log.WithFields(log.Fields{
		"method": req.Method,
		"path":   req.URL.Path,
		"proto":  req.Proto,
	})

	// set this so that it correctly sets it later
	req.Host = ""

	// Log request headers
	headers := make(map[string]string)
	for key, val := range req.Header {
		headers[key] = strings.Join(val, ",")
	}
	requestLogger = requestLogger.WithField("headers", headers)

	// Handle request body
	var requestBody []byte
	if req.Body != nil {
		defer req.Body.Close()
		var err error
		requestBody, err = io.ReadAll(req.Body)
		if err != nil {
			requestLogger.WithError(err).Error("Failed to read request body")
			return nil, err
		}
		req.Body = io.NopCloser(bytes.NewBuffer(requestBody))

		fmt.Println(string(requestBody))
		// requestLogger = requestLogger.WithField("body", string(requestBody))
	}

	requestLogger.Info("Incoming request")

	// Make the actual request using our HTTP/1.1 transport
	resp, err := t.transport.RoundTrip(req)
	if err != nil {
		requestLogger.WithError(err).Error("Failed to make request")
		return nil, err
	}

	// Create response logger
	responseLogger := requestLogger.WithFields(log.Fields{
		"status_code": resp.StatusCode,
		"duration_ms": time.Since(startTime).Milliseconds(),
	})

	// Log response headers
	respHeaders := make(map[string]string)
	for key, val := range resp.Header {
		respHeaders[key] = strings.Join(val, ",")
	}
	responseLogger = responseLogger.WithField("headers", respHeaders)

	// Handle response body
	if resp.Body != nil {
		defer resp.Body.Close()
		respBody, err := io.ReadAll(resp.Body)
		if err != nil {
			responseLogger.WithError(err).Error("Failed to read response body")
			return nil, err
		}
		resp.Body = io.NopCloser(bytes.NewBuffer(respBody))

		var bodyContent []byte
		if resp.Header.Get("Content-Encoding") == "gzip" {
			reader := bytes.NewReader(respBody)
			gzreader, err := gzip.NewReader(reader)
			if err != nil {
				responseLogger.WithError(err).Error("Failed to read gzip encoded message")
				return nil, err
			}

			bodyContent, err = io.ReadAll(gzreader)
			if err != nil {
				responseLogger.WithError(err).Error("Failed to extract gzip encoded message")
				return nil, err
			}
			responseLogger = responseLogger.WithField("content_encoding", "gzip")
		} else {
			bodyContent = respBody
		}

		fmt.Println(string(bodyContent))
		//responseLogger = responseLogger.WithField("body", string(bodyContent))
	}

	responseLogger.Info("Response received")

	return resp, nil
}

func (h *ProxyHandler) ProxyRequest(w http.ResponseWriter, r *http.Request) {
	log.WithFields(log.Fields{
		"method": r.Method,
		"url":    r.URL.String(),
		"proto":  r.Proto,
	}).Info("Handling proxy request")
	h.Proxy.ServeHTTP(w, r)
}

func main() {
	var svrAddr string // proxy server ip
	var destUrlStr string
	var logLevel string

	flag.StringVar(&svrAddr, "p", "0.0.0.0:9090", "Proxy Server Address")
	flag.StringVar(&destUrlStr, "d", "http://localhost:8080", "destination url")
	flag.StringVar(&logLevel, "log-level", "info", "Log level (debug, info, warn, error)")

	flag.Parse()

	log.SetFormatter(&log.TextFormatter{
		FullTimestamp: true,
	})

	// Set log level
	level, err := log.ParseLevel(logLevel)
	if err != nil {
		log.WithError(err).Fatal("Invalid log level")
	}
	log.SetLevel(level)

	log.WithFields(log.Fields{
		"proxy_addr":      svrAddr,
		"destination_url": destUrlStr,
		"log_level":       logLevel,
	}).Info("Starting proxy server")

	// Create a proxy instance with given params
	destUrl, err := url.Parse(destUrlStr)
	if err != nil {
		log.Fatalf("Unable to parse destination url: %s", destUrlStr)
	}

	// Create a new ProxyHandler
	proxyHandler := NewProxyHandler(destUrl)

	// Register a handler function
	http.HandleFunc("/", proxyHandler.ProxyRequest)

	// Start a proxy server
	err = http.ListenAndServe(svrAddr, nil)
	if err != nil {
		panic(err)
	}
}
